# LLM 智能网关（AI Gateway）技术方案设计文档

| 项 | 内容 |
|---|---|
| 文档版本 | v1.0（评审稿） |
| 状态 | 待技术评审 |
| 日期 | 2026-09-12 |
| 作者 | 后端组 |
| 评审人 | 架构组 / SRE / 安全 / 财务（成本） |
| 相关系统 | 模型服务（OpenAI 兼容协议）、账号与租户中心、计费系统、配置中心 |

---

## 1. 背景与目标

### 1.1 背景

业务线已接入多家大模型供应商（自建推理 + OpenAI 兼容第三方）。当前各业务直连供应商 API，存在以下问题：

1. **无统一管控**：各业务自行申请 Key，配额、成本、审计不可见；
2. **无稳定性防护**：供应商 429/5xx/超时直接透传用户，无熔断、降级、兜底；
3. **无成本治理**：Token 消耗无租户维度归集，超支不可控；
4. **协议碎片化**：各供应商出入参有小差异，业务侧重复适配。

### 1.2 目标

建设统一 LLM 网关，对内暴露 **OpenAI 兼容协议**，对外承担多供应商适配、稳定性防护与成本治理。

| 类别 | 目标 |
|---|---|
| 功能 | OpenAI 兼容 API（chat/completions 流式+非流式）、多供应商路由与 fallback、五维限流、熔断、四级降级、Token 计量与对账 |
| 非功能 | 详见 §7 容量与 SLO |
| 非目标（本期不做） | 提示词管理/版本化、模型评测平台、Agent 编排、向量检索服务（仅预留接口） |

### 1.3 成功指标

- 供应商故障时用户侧无感知降级比例 ≥ 99%（L1/L2 降级可承接）；
- 单租户成本超支可在 1 分钟内被限流阻断；
- 网关自身可用性 ≥ 99.95%，自身延迟开销（TTFT 增量）≤ 50ms（P99）。

---

## 2. 名词解释

| 名词 | 含义 |
|---|---|
| Provider | 模型供应商（自建 vLLM / DeepSeek / 阿里百炼等） |
| Model | 模型标识（如 gpt-5、deepseek-v3.2），网关内部统一命名，映射到供应商模型名 |
| 五维配额 | API Key / 租户 / 模型 / Provider / Token 五个限流维度 |
| 降级链 | 主模型 → 备用模型 → 小模型精简 → 语义缓存 → 兜底文案 |
| TTFT | 首 Token 延迟；TTFB 为其 HTTP 层近似 |
| Reconcile | 计量对账：按供应商返回的真实 usage 修正预估扣费 |

---

## 3. 总体设计

### 3.1 架构图

```
                    ┌──────────────────────────────────────────────────┐
   Client ──HTTPS──▶│                    API 层                        │
                    │  /v1/chat/completions (OpenAI 兼容)              │
                    │  鉴权(API Key→租户) / 参数校验 / 审计日志          │
                    └───────────────┬──────────────────────────────────┘
                                    │
                    ┌───────────────▼──────────────────────────────────┐
                    │              治理层（核心）                        │
                    │  ① 限流: 五维配额(Redis+Lua, 滑动窗口/令牌桶)      │
                    │  ② Token 预算: estimate→reserve→reconcile        │
                    │  ③ 熔断: 模型粒度, 错误率+慢调用比例(本地状态机)   │
                    │  ④ 降级决策: 四级降级链 + 动态开关                │
                    │  ⑤ 过载保护: 有界队列 + worker pool + 背压      │
                    └───────────────┬──────────────────────────────────┘
                                    │
                    ┌───────────────▼──────────────────────────────────┐
                    │            模型接入层                              │
                    │  Provider Adapter(协议转换) / 连接池与连接复用     │
                    │  重试(全抖动退避, retry budget ≤10%) / 流式续写降级│
                    └───────────────┬──────────────────────────────────┘
                                    │
                    ┌───────────────▼──────────────────────────────────┐
                    │  Provider A (自建)   Provider B (DeepSeek)  ...    │
                    └──────────────────────────────────────────────────┘

  横切: Prometheus 指标 / OTel  tracing / Langfuse LLM 追踪 / 配置中心
```

### 3.2 关键设计取舍（评审重点）

| 取舍 | 决策 | 理由 |
|---|---|---|
| 限流计数存本地 or Redis | **Redis（多副本强一致需求）**，Redis 故障时降级为单机限流 | 多副本网关各自计数等于没限；本地态可用性更高 |
| 限流故障策略 fail-open or fail-close | **限流判定 fail-open，配额结算 fail-close（挂账恢复）** | 保可用优先；钱可事后对账，不可用直接损失用户 |
| 熔断统计存本地 or Redis | **本地内存**（每副本独立） | 熔断追求快（ms 级），跨副本一致无收益；各副本负载不同，独立熔断更精准 |
| Token 预估 or 实测计费 | **预扣 + 实测对账（异步）** | 流式场景结束才知道真实 usage，必须预扣防透支 |
| SSE 中途降级 | **续写而非重试**：已生成上文作为 assistant 前缀发给备用模型 | 重试贵且用户可见重复输出 |
| 网关语言 | Go（gin + gorilla/httpx，SSE 用原生 http.Flusher） | 团队主语言，高并发 IO 亲和 |

### 3.3 核心链路时序（一次流式请求）

```
Client ──POST /v1/chat/completions──▶ API 层: 鉴权/校验/审计
API 层 ──Acquire(tenant,key,model,token预算)──▶ 限流器(Redis Lua×5维, pipeline)
   │ 拒绝 ──▶ 按策略: 降级L2/L3 或 429+Retry-After
   ▼ 放行
治理层 ──熔断检查(本地, model粒度)──▶ Open? ──▶ 直接走降级链L1
治理层 ──提交有界队列──▶ worker pool(按 model 分池, 池满→排队或快速失败)
worker ──Provider Adapter──▶ 供应商(流式 SSE)
worker ◀──delta 流── 逐 chunk 转发客户端; 全程 ctx 联动(客户端断开→取消上游)
worker ──结束/中断──▶ usage 上报 → 异步 Reconcile 修正配额 → 指标打点
异常路径: 429→立即换渠道重试; 5xx/网络错误→jitter退避重试(≤2次); 熔断/超时→降级链
```

---

## 4. 详细设计

### 4.1 API 层

- 兼容 OpenAI `POST /v1/chat/completions`，支持 `stream=true/false`；响应逐字节兼容（`choices[].delta`、`finish_reason`、`usage`、`data: [DONE]`）。
- 鉴权：`Authorization: Bearer ak-xxx` → 解析出 keyId/tenantId，Redis 缓存 Key 元数据（TTL 5min），失效回源 MySQL。
- 参数校验 + 默认值：max_tokens 上限按模型配置裁剪，temperature/top_p 白名单校验，防止异常参数放大成本。
- 审计：请求元数据（key/tenant/model/输入token数/是否降级/耗时/usage）异步写 MQ → 落 MySQL 流水表，**不在请求热路径同步写库**。

### 4.2 限流模块

**配额模型**（五维，任一维度触顶即触发对应策略）：

| 维度 | Key 模式 | 算法 | 典型阈值（示例，可配置） |
|---|---|---|---|
| API Key | `rl:key:{keyId}` | 滑动窗口 | 60 RPM |
| 租户 | `rl:tnt:{tenantId}` | 滑动窗口 | 1000 RPM / 5M TPM |
| 模型 | `rl:mdl:{model}` | 令牌桶 | 桶容量=模型并发容量 |
| Provider | `rl:pvd:{provider}` | 令牌桶 | 与上游配额对齐，预留 20% 缓冲 |
| Token | `rl:tkn:{tenantId}` | 滑动窗口（预扣制） | TPM 预算 |

**实现要点**：
1. 单次请求五维检查用 Redis **pipeline 批量提交**，一个 RTT 完成；脚本预编译缓存（`SCRIPT LOAD`）。
2. 滑动窗口用 zset（member=毫秒时间戳+随机数），窗口边界清理过期成员；`PEXPIRE` 兜底防脏 key。
3. 模型/Provider 维度的令牌桶可用 Redis `INCRBY + EXPIRE` 近似（固定窗口令牌桶），精度损失可接受，换取 O(1) 性能。
4. 热点 key 预估：峰值 5000 QPS × 5 维 ≈ 2.5 万次脚本/s 单实例 Redis 扛不住 → **配额决策加本地缓存短窗预聚合**（10ms 内同租户同模型合并判定），超限才精确化。

**被限流后的策略路由**（配置化）：
- 贵模型超限 → 自动降级 L2（小模型）；
- 轻量模型超限 → 排队（队列未满）或 429 + `Retry-After`；
- Provider 维超限 → 换 Provider（同模型映射）；
- 试运行租户 → 仅告警不阻断。

### 4.3 Token 计量（estimate → reserve → reconcile）

1. **Estimate**：tokenizer（tiktoken Go 版/近似算法）预估 `input + min(max_tokens, 模型上限)` 作为预留量；
2. **Reserve**：五维桶原子扣减预留量（Redis Lua：扣减成功返回 permitId，预扣额写入 `pending:{permitId}`）；
3. **实际 usage**：流正常结束取供应商 `usage`；中断时按已产出 delta 估算或保守挂账；
4. **Reconcile**（异步，MQ 消费）：`实际释放 = 预留 - 实际`，回补租户 Token 桶；对账差异 > 20% 告警（tokenizer 与供应商口径漂移）。

幂等与一致性：permitId 全局唯一（ULID），reconcile 消费端按 permitId 幂等去重（Redis SETNX 7d）。

### 4.4 熔断模块

- **粒度**：`model`（内部统一模型名），各副本独立熔断；
- **策略**（sentinel-golang 语义，规则配置化）：
  - ErrorRatio：10s 窗口错误率 > 40% 且请求数 ≥ 20 → Open；
  - SlowRequestRatio：RT > 15s（该模型 TTFT+生成 P99 基线）比例 > 50% 且请求数 ≥ 20 → Open；
- **状态机**：Open 冷却 30s → Half-Open（放 5 个探测请求）→ 成功 Closed / 失败回 Open；
- **错误分类计数**：429 不计入熔断（属配额问题，走换渠道重试）；5xx/网络错误/超时计入；
- 熔断打开期间请求**直接短路到降级链 L1**，不占用 worker。

### 4.5 降级模块

四级降级链（配置化启停与阈值）：

| 级别 | 动作 | 触发条件 |
|---|---|---|
| L1 | 同能力备用模型（跨 Provider） | 主模型 429/5xx/熔断 |
| L2 | 小模型 + 精简 prompt（去工具调用/RAG、截断上下文、降 max_tokens） | L1 不可用 / 租户超 Token 预算 |
| L3 | 语义缓存（embedding 相似度 > 0.95 且未过期） | L2 不可用；正常路径也先查缓存 |
| L4 | 兜底文案/业务模板 | 全部不可用 |

- **流式中途降级**：记录已生成 delta 序列；断流时将上文组装为 `assistant` 前缀，带 `continue` 指令发往 L1 模型续写，用户无感。
- **降级开关**：配置中心动态下发，支持按租户/按模型/按比例灰度；开关变更 10s 内全网生效。
- 每次降级打指标 `gateway_degradation_total{level="L1",model=...,tenant=...}` 并 Warn 级日志（可观测要求：降级必须可观测）。

### 4.6 过载保护与背压

1. **有界队列 + worker pool**：入口先进 `chan *Request`（容量 5000/副本），按 model 分 worker 池；池满 + 队列满 → fail-fast 返回 503 + Retry-After（宁可拒绝不可拖死）。
2. **背压**：SSE 转发链路 client ← chan ← upstream，channel 缓冲 256 chunk；客户端慢消费导致缓冲满 → 暂停读上游（天然 TCP 背压），超过 30s 判定慢客户端，主动断开释放资源。
3. **自适应限流**：采集 Goroutine 数 / 内存 / 队列水位，超阈值自动按比例丢弃新请求（load shedding）。

### 4.7 模型路由与 Provider Adapter

- 统一内部模型名（`gpt-5`），路由表配置：`model → [{provider, 权重, 优先级, 成本档}]`；
- Adapter 接口负责出入参转换（含各家 usage 字段差异归一）；新增供应商 = 新增 Adapter，不动主链路；
- 连接管理：每 Provider 连接池（`http.Transport` MaxConnsPerHost 按配额配置），复用连接降低 TTFT。

---

## 5. 存储设计

**Redis**（限流/熔断旁路/缓存，全量可重建，不存关键业务数据）：

| Key | 结构 | TTL |
|---|---|---|
| `rl:{dim}:{id}` | zset（滑动窗口）/ string（令牌桶） | 窗口 2 倍 |
| `pending:{permitId}` | string（预扣 JSON） | 1h |
| `keymeta:{keyId}` | hash（Key 元数据缓存） | 5min |
| `semcache:{hash}` | string（语义缓存答案） | 按业务 1–24h |

**MySQL**（审计流水、配额配置、对账结果）：
- `llm_request_log`：请求流水（异步批量插入，按天分区）；
- `quota_config` / `model_route`：配额与路由配置（配置中心为权威源，DB 为快照）；
- `reconcile_diff`：对账差异表。

---

## 6. 可观测设计

| 层 | 内容 |
|---|---|
| Metrics（Prometheus） | `gateway_requests_total{model,provider,tenant,code}`、`gateway_ttft_seconds` / `gateway_e2e_seconds`（histogram）、`gateway_tokens_total{direction}`、`gateway_cost_usd_total`、`gateway_circuit_breaker_state{model}`、`gateway_degradation_total{level}`、`gateway_queue_depth`、`gateway_rate_limited_total{dim}` |
| Tracing（OTel） | traceId 贯穿 客户端→网关→Adapter→供应商回调；Langfuse 专项记录 Prompt/Output/Token/模型版本 |
| 看板 | 租户成本大盘（token/费用/429 率）、模型健康度（TTFT P99/错误率/熔断状态）、降级触发趋势 |
| 告警 | 熔断 Open、降级率 > 1%、TTFT P99 突增 3 倍、单租户 5min 成本 > 阈值、Redis 限流异常率 > 0.5% |

---

## 7. 非功能性设计

### 7.1 容量估算（示例，压测校准）

| 项 | 估算 |
|---|---|
| 峰值 QPS | 2000（HPA 上限 20 副本） |
| 平均耗时 | 8s/请求（流式为主）→ 峰值 in-flight ≈ 16,000，跨副本分摊 800/副本 |
| 每请求 Redis 操作 | 5 维 pipeline ≈ 1 RTT + reconcile 异步 1 次 → 峰值 4000 次/s，主从 + Cluster 单分片可扛（加本地预聚合后降至约 400/s） |
| 内存 | 队列 5000×2KB + worker 上下文 ≈ 300MB/副本，限 512MB _requests 水位告警_ |
| 网关自身开销 | TTFT 增量 ≤ 50ms P99（鉴权缓存命中 + pipeline 1 RTT） |

### 7.2 可用性

- 网关无状态（状态全在 Redis/配置中心），K8s 多副本 + HPA + PodDisruptionBudget；
- Redis 故障：限流降级为单机令牌桶（fail-open），熔断/降级不依赖 Redis 照常工作；
- 配置中心故障：本地快照兜底，最后一次配置继续生效。

### 7.3 安全

- Key 不落日志明文（脱敏 ak-****1234）；TLS 全链路；
- 单请求 max_tokens / 上下文长度硬上限，防成本攻击（prompt bomb）；
- 租户隔离：Redis key 带租户前缀，Adapter 侧携带独立供应商 Key，禁止跨租户复用；
- 审计流水保留 180 天，支持成本回溯与安全审计。

---

## 8. 部署架构（K8s）

```
Deployment: ai-gateway (replicas 4→20, HPA: CPU 60% / 队列水位自定义指标)
  ├─ readiness: /healthz（依赖 Redis ping 失败不阻塞，打降级标记）
  ├─ liveness: /livez
  ├─ resources: requests 0.5c/512Mi, limits 1c/1Gi
  └─ ConfigMap + Nacos 双源配置
Service: ClusterIP + Ingress(Nginx, SSE: proxy_buffering off, read timeout 600s)
Redis: 主从 + Sentinel（限流/缓存），与业务 Redis 集群物理隔离
```

SSE 必配：`proxy_buffering off;` `proxy_read_timeout 600s;` `http2` 多路复用规避浏览器 6 连接限制。

---

## 9. 故障场景分析（FMEA）

| 故障 | 影响 | 检测 | 缓解 | 恢复 |
|---|---|---|---|---|
| 主模型 5xx/超时 | 该模型请求失败 | 熔断指标 | L1 备用模型接管 | 熔断半开自动恢复 |
| 供应商 429 | 延迟升高 | 429 率告警 | 换渠道重试；Provider 桶提前限流 | 配额缓冲 20% |
| Redis 宕机 | 限流失准 | Redis 告警 | 单机限流 fail-open；结算挂账 | 哨兵切换，挂账对账 |
| 客户端大量慢连接 | goroutine/内存膨胀 | 队列水位/慢客户端指标 | 背压 + 30s 主动断开 | 自动 |
| 单租户流量突增（热点） | 挤占共享配额 | 租户 QPS 告警 | 租户维度配额硬限 + 本地预聚合 | 配置调整 |
| 网关副本 OOM | 部分 5xx | 内存水位 | 资源限额 + HPA；队列有界防雪崩 | 自动重启 |
| 配置误下发（阈值过低） | 大面积误限流 | 限流率突增告警 | 配置版本化 + 一键回滚（Nacos） | 回滚 ≤ 1min |
| 语义缓存污染 | 错误答案命中 | 缓存命中率异常 + 用户反馈 | 缓存按租户/模型隔离，可一键清空 | 清空重建 |

---

## 10. 发布计划与灰度

| 阶段 | 内容 | 出口标准 |
|---|---|---|
| M1（1 周） | API 层 + 流式转发 + 单 Provider 适配 | 功能测试通过，TTFT 增量 ≤ 50ms |
| M2（1.5 周） | 五维限流 + Token 预算对账 | 压测 2000 QPS，误限率 < 0.1%，对账差异 < 5% |
| M3（1 周） | 熔断 + 四级降级 + 开关体系 | 故障演练：杀主模型流量，L1 接管无感 |
| M4（1 周） | 全量观测 + 灰度上线 | 影子流量 7 天对比，核心指标无损后切 10%→50%→100% |

回滚：Ingress 权重切回直连（保留旧链路双跑 2 周）；配置类问题 Nacos 版本回滚。

---

## 11. 风险与开放问题

| # | 风险/开放问题 | 应对 | 决策人 |
|---|---|---|---|
| R1 | tokenizer 与供应商 usage 口径漂移导致对账差异 | 差异 > 20% 告警 + 按供应商校准系数 | 后端 |
| R2 | 语义缓存的命中率与新鲜度平衡 | 先只对"问答类低时效"场景启用 | 架构 |
| R3 | 自建推理 Provider 的容量画像不准，模型维度桶阈值拍脑袋 | M2 压测出容量基线后再定阈值 | SRE |
| R4 | 流式续写降级在个别供应商不支持前缀续写 | 降级 L1 选型时列为硬性要求 | 后端 |
| R5 | Redis Cluster 热点分片（全局 Provider 桶） | 桶值本地预聚合 + 定期同步，弱化全局桶精度 | 架构 |

---

## 12. 评审检查单（Checklist）

- [ ] 容量估算数字是否经压测校准（M2 出口复核）
- [ ] 五维限流阈值表与各业务方确认（财务会签成本侧）
- [ ] 熔断阈值（慢调用 RT 基线）有真实数据支撑
- [ ] 降级链 L1 备用模型的数据合规性（跨境供应商 → 安全评审）
- [ ] Redis 故障演练计划纳入 M4
- [ ] 审计流水保留策略符合合规（180 天）
- [ ] 灰度期间旧链路双跑的资源成本确认

---

*本方案评审通过后进入开发；变更需走设计变更评审（变更记录附文末）。*
