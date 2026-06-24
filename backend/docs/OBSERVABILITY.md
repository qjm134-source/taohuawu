# 可观测性指南

本项目提供完整的 **Metrics + Traces + Logs + LLM 专项可观测** 能力，基于 **Prometheus** 暴露指标、**OpenTelemetry** 采集链路、**Langfuse** 专项追踪 LLM 调用，并通过 **审计日志** 记录关键操作。你可以用它监控 LLM 调用成本、WebSocket 连接状态、HTTP 接口性能、缓存命中率以及全链路延迟。

---

## 1. 架构概览

```
┌────────────────────────────────────────────────────────────┐
│                      业务代码                               │
│  Agent Runtime / WebSocket Handler / HTTP Handler          │
│       │                            │                      │
│       ▼                            ▼                      │
│  Prometheus 指标                 OpenTelemetry Spans       │
│       │                            │                      │
│       ▼                  ┌─────────┴─────────┐             │
│  GET /metrics            │  OTLP/HTTP 上报    │ stdout 打   │
│  (可配置关闭)             │  (→ Jaeger)       │ 印到日志   │
│       │                  └──────────────────┘             │
│       ▼                                                   │
│  Prometheus 时序库                                        │
│       │                                                   │
│       ▼                                                   │
│  Grafana 看板                                             │
└────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────┐
│                    Langfuse LLM 可观测                      │
│  记录每次 LLM 调用的完整上下文：                            │
│  - 输入/输出内容                                           │
│  - Token 消耗                                              │
│  - 成本估算                                                │
│  - 响应延迟                                                │
│  - 用户评分                                                │
│                                                            │
│  数据存储：Langfuse Cloud 或自建服务                       │
│  查看方式：Langfuse Web Dashboard                          │
└────────────────────────────────────────────────────────────┘
```

- **Metrics**：告诉你系统"发生了什么"，例如 QPS、延迟、错误率、Token 消耗、成本、缓存命中率。
- **Traces**：告诉你"一次请求经历了什么"，例如 WebSocket 消息经过哪些函数、每个阶段耗时多久。
- **LLM 专项可观测（Langfuse）**：告诉你"每次 LLM 调用的完整细节"，包括输入 Prompt、输出内容、Token 数、成本、用户反馈。
- **Logs**：审计日志记录谁在什么时间做了什么操作，已在 `/api/v1/audit` 提供查询。

---

## 2. 配置开关

所有可观测性组件都可以通过配置文件独立开关：

```yaml
# configs/config.yaml
observability:
  enabled: true                   # 总开关（影响 Tracing）
  service_name: watertown-guide
  endpoint: http://localhost:4318 # OTLP HTTP 端口
  sample_rate: 1.0                # 采样率，1.0=全量
  trace_exporter: stdout          # stdout（开发）| otlp（生产）
  prometheus: true                # 是否启用 Prometheus 指标（/metrics 端点）
  
  langfuse:
    enabled: false                # 是否启用 Langfuse LLM 专项可观测
    host: https://cloud.langfuse.com
    public_key: ${LANGFUSE_PUBLIC_KEY}
    secret_key: ${LANGFUSE_SECRET_KEY}
```

| 配置项 | 说明 | 默认值 |
|-------|------|--------|
| `observability.enabled` | 总开关，关闭后 Tracing 不生效 | `true` |
| `observability.prometheus` | Prometheus 指标开关，关闭后 `/metrics` 不可访问 | `true` |
| `observability.langfuse.enabled` | Langfuse LLM 专项追踪开关 | `false` |

**关闭 Prometheus 的场景**：
- 开发调试时不需要监控指标
- 减少内存占用和性能开销
- 不需要外部监控系统

**关闭方式**：
```yaml
observability:
  prometheus: false
```

---

## 3. Prometheus 指标清单

启动服务后，通过 `GET /metrics` 获取所有指标（需启用 `prometheus: true`）。

### 3.1 HTTP 层指标（中间件自动采集）

| 指标名称 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `http_requests_total` | Counter | `method`, `path`, `status` | HTTP 请求总数 |
| `http_request_duration_seconds` | Histogram | `method`, `path` | HTTP 请求耗时分布 |
| `http_requests_in_flight` | Gauge | - | 当前正在处理的 HTTP 请求数 |

### 3.2 Agent 层指标

| 指标名称 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `agent_requests_total` | Counter | `action`, `status` | Agent 调用次数：`action=welcome/chat`，`status=success/error` |
| `agent_request_duration_seconds` | Histogram | `action` | Agent 处理耗时 |

### 3.3 LLM 层指标

| 指标名称 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `llm_requests_total` | Counter | `model`, `status` | 每个模型的调用次数与状态 |
| `llm_request_duration_seconds` | Histogram | `model` | 每个模型的调用耗时 |
| `llm_request_tokens_total` | Counter | `model` | 每个模型的输入 Token 累计 |
| `llm_completion_tokens_total` | Counter | `model` | 每个模型的输出 Token 累计 |
| `cost_total` | Counter | `model` | 每个模型的累计成本（美元） |

### 3.4 WebSocket 层指标

| 指标名称 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `websocket_connections` | Gauge | `tenant_id` | 每个租户的当前活跃连接数 |
| `websocket_messages_total` | Counter | `type`, `direction` | WebSocket 消息总数：`direction=in/out` |

### 3.5 缓存层指标

| 指标名称 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `cache_hits_total` | Counter | `cache_type` | 缓存命中次数（精确匹配/语义匹配） |
| `cache_misses_total` | Counter | `tenant_id` | 缓存未命中次数 |
| `cache_hit_ratio` | Gauge | `tenant_id` | 缓存命中率 |

---

## 4. OpenTelemetry 分布式追踪

系统通过 OTLP/HTTP 协议将 Trace 上报到支持 OTLP 的 Collector（如 Jaeger、Grafana Tempo），也支持直接输出到控制台日志（开发模式）。

### 4.1 配置

```yaml
# configs/config.yaml
observability:
  enabled: true                   # 是否启用追踪
  service_name: watertown-guide
  endpoint: http://localhost:4318 # OTLP HTTP 端口
  sample_rate: 1.0                # 采样率，1.0=全量，0.1=10%
  trace_exporter: stdout          # stdout（开发）| otlp（生产，发送到 Jaeger）
```

**两种导出模式：**

| `trace_exporter` | 行为 | 适用场景 |
|---|---|---|
| `stdout` | 每个 Span 完成后以可读 JSON 打印到标准输出 | 开发调试，无需外部组件 |
| `otlp` | 通过 OTLP/HTTP 发送到 `endpoint` 指向的 Collector | 生产环境，配合 Jaeger/Grafana 查看 |

### 4.2 已接入的 Span

| 位置 | Span 名称 | 说明 |
|---|---|---|
| `middleware.go` | `GET /path` / `POST /path` | 每个 HTTP 请求自动创建 Server Span |
| `runtime.go` | `HandleWelcome` | 欢迎消息处理 |
| `runtime.go` | `HandleChat` | 普通对话处理 |
| `runtime.go` | `HandleChatStream` | 流式对话处理 |

`TracingMiddleware` 还会从 HTTP 请求头中提取 `traceparent`，并在响应头中注入 `traceparent`，方便前端或网关关联同一 Trace。

---

## 5. Langfuse LLM 专项可观测

### 5.1 什么是 Langfuse？

Langfuse 是专门为 LLM 应用设计的开源可观测平台，可以记录每次 LLM 调用的完整上下文：

```
OTel Span（现在）          Langfuse Trace（加上后）
─────────────────────     ─────────────────────────────
model: "claude"            model: "claude"
duration: 1.2s             duration: 1.2s
status: "ok"               input_tokens: 2456
                           output_tokens: 512
                           cost: $0.032
                           prompt: "你是一个江南水乡导游..."
                           response: "您好，欢迎来到周庄！..."
                           prompt_version: v2.3
                           user_feedback: 👍
```

### 5.2 数据存储位置

Langfuse 数据存储在 **Langfuse 云端或自建服务**，而不是本地：

| 方案 | 数据存储 | 查看方式 | 适用场景 |
|------|---------|---------|---------|
| **Langfuse Cloud** | 云端托管 | [cloud.langfuse.com](https://cloud.langfuse.com) | 不想自运维、快速接入 |
| **Langfuse 自建** | 本地 PostgreSQL | `http://localhost:3000` | 有运维能力的团队 |

### 5.3 启用 Langfuse

**方式一：Langfuse Cloud（推荐）**

1. 在 [Langfuse 官网](https://cloud.langfuse.com) 注册账号
2. 获取 API Key（PublicKey 和 SecretKey）
3. 设置环境变量：

```bash
export LANGFUSE_PUBLIC_KEY=pk-your-key
export LANGFUSE_SECRET_KEY=sk-your-key
```

4. 修改配置文件：

```yaml
observability:
  langfuse:
    enabled: true
    host: https://cloud.langfuse.com
```

5. 启动服务后，日志会显示：
```
[Langfuse] Enabled — sending LLM traces to https://cloud.langfuse.com
```

6. 访问 [Langfuse Dashboard](https://cloud.langfuse.com) 查看 Trace

**方式二：Langfuse 自建**

```yaml
# docker-compose.yml 添加
langfuse-server:
  image: ghcr.io/langfuse/langfuse:latest
  container_name: watertown-langfuse
  ports:
    - "3001:3000"
  environment:
    DATABASE_URL: postgresql://postgres:postgres@langfuse-db:5432/postgres
    NEXTAUTH_SECRET: mysecret
    SALT: mysalt
  depends_on:
    - langfuse-db

langfuse-db:
  image: postgres:16
  environment:
    POSTGRES_USER: postgres
    POSTGRES_PASSWORD: postgres
  volumes:
    - langfuse_db:/var/lib/postgresql/data
```

然后配置：
```yaml
observability:
  langfuse:
    enabled: true
    host: http://localhost:3001
```

### 5.4 Langfuse 记录的数据

每次 LLM 调用会记录：
- **输入/输出**：完整的对话内容
- **Token 数量**：输入/输出 tokens
- **成本**：调用费用估算
- **延迟**：响应时间
- **模型名称**：使用的 LLM 模型
- **错误信息**：如果调用失败
- **用户评分**：用户反馈（可选）

---

## 6. 缓存系统可观测

### 6.1 多层缓存架构

项目实现了多层缓存系统，每层都有独立的指标：

```
┌─────────────────────────────────────────────────────────────┐
│                    客户端请求                               │
└────────────────────────────────┬────────────────────────────┘
                                 │
                                 ▼
┌─────────────────────────────────────────────────────────────┐
│  第一层：精确匹配缓存 (Exact Match)                        │
│  缓存键：SHA256(question:model)                            │
│  命中率：60-80%（FAQ 场景）                                 │
└────────────────────────────────┬────────────────────────────┘
                                 │ 未命中
                                 ▼
┌─────────────────────────────────────────────────────────────┐
│  第二层：语义缓存 (Semantic Cache)                         │
│  使用 Embedding 向量相似度匹配                             │
│  相似度阈值：0.85                                          │
│  命中率：额外 10-20%                                        │
└────────────────────────────────┬────────────────────────────┘
                                 │ 未命中
                                 ▼
┌─────────────────────────────────────────────────────────────┐
│  第三层：工具结果缓存 (Tool Result Cache)                  │
│  缓存键：SHA256(tool_name:params)                          │
│  命中率：70-90%（相同参数的工具调用）                       │
└────────────────────────────────┬────────────────────────────┘
                                 │ 未命中
                                 ▼
                          LLM API 调用
```

### 6.2 缓存配置

```yaml
cost:
  cache_enabled: true
  similarity_threshold: 0.85    # 语义相似度阈值
  embedding:
    enabled: true
    type: remote               # remote（OpenAI API）| local（本地模型）
    api_key: ${OPENAI_API_KEY}
    base_url: ""
    model: text-embedding-3-small
```

### 6.3 缓存指标查询

```promql
# 缓存命中率
cache_hits_total / (cache_hits_total + cache_misses_total)

# 各类型缓存命中次数
sum by (cache_type) (cache_hits_total)

# 缓存未命中次数（按租户）
sum by (tenant_id) (cache_misses_total)
```

---

## 7. 快速查看指标与链路

### 7.1 裸看 `/metrics`

```bash
# 启动服务后访问（需启用 prometheus: true）
curl http://localhost:8080/metrics
```

输出示例：

```
# HELP http_requests_total Total number of HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET",path="/api/health",status="200"} 42

# HELP cache_hits_total Cache hits
# TYPE cache_hits_total counter
cache_hits_total{cache_type="exact"} 150
cache_hits_total{cache_type="semantic"} 25
```

### 7.2 Prometheus + Grafana（推荐生产方案）

项目根目录已提供 `prometheus.yml` 和 `docker-compose.yml` 集成。Prometheus 自动每 15 秒从 `backend:8080/metrics` 拉取一次指标，数据持久化到 Docker Volume `prometheus_data`，默认保留 15 天。

```bash
# 一键启动（含 Prometheus）
docker-compose up -d

# 或者只起 Prometheus
docker-compose up -d prometheus
```

**直接在 Prometheus Web UI 中写 PromQL**（无需 Grafana）：

打开 `http://localhost:9090` → 顶部菜单 **Graph** → 输入 PromQL 如：
```
rate(http_requests_total[1m])
```
点 **Execute** 即可看折线图或表格。

**搭配 Grafana**（如果希望更美观的 Dashboard）：

```bash
docker run -d --name grafana -p 3000:3000 grafana/grafana
```

然后在 Grafana 中添加 Prometheus 数据源（URL 填 `http://host.docker.internal:9090`），创建 Dashboard。

### 7.3 开发模式：直接看 Trace 日志（无需额外组件）

如果只想在开发时快速查看 Trace，**不需要启动任何外部组件**，配置 `trace_exporter: stdout` 即可。

```yaml
# configs/config.yaml
observability:
  enabled: true
  trace_exporter: stdout    # 关键：改为 stdout
  sample_rate: 1.0
```

启动服务后，每个请求的 Span 会以可读 JSON 格式输出到控制台/日志文件：

```json
{
  "Name": "GET /api/chat",
  "SpanContext": {
    "TraceID": "a1b2c3d4e5f6...",
    "SpanID": "1a2b3c4d5e6f..."
  },
  "Parent": {"SpanID": "0000000000000000"},
  "SpanKind": 1,
  "StartTime": "2026-06-24T10:30:00.123Z",
  "EndTime": "2026-06-24T10:30:01.456Z",
  "Attributes": [
    {"Key": "http.method", "Value": {"Type": "STRING", "Value": "GET"}},
    {"Key": "http.status_code", "Value": {"Type": "INT64", "Value": 200}}
  ],
  "Events": [],
  "Status": {"Code": "Unset"}
}
```

同一个 Trace 的所有 Span 共享 `TraceID`，可以通过 `TraceID` 把父子 Span 关联起来。

### 7.4 Jaeger（生产级可视化链路）

需要将 `trace_exporter` 改为 `otlp`，并保持 `endpoint` 指向 Jaeger 的 OTLP HTTP 端口。

```bash
# 启动 Jaeger（支持 OTLP HTTP on :4318）
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4318:4318 \
  jaegertracing/all-in-one:latest
```

1. 修改 `configs/config.yaml`：`trace_exporter: otlp`，`enabled: true`。
2. 重启服务并触发几个请求。
3. 打开 `http://localhost:16686`，搜索 Service `watertown-guide`，即可查看 Trace 瀑布图。

---

## 8. 常用 PromQL 查询

```promql
# HTTP QPS（按路径）
sum by (path) (rate(http_requests_total[1m]))

# HTTP P99 延迟
histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket[1m])) by (le, path))

# LLM 调用错误率
sum(rate(llm_requests_total{status="error"}[5m])) / sum(rate(llm_requests_total[5m]))

# 各模型 Token 消耗速率
sum by (model) (rate(llm_request_tokens_total[5m]))
sum by (model) (rate(llm_completion_tokens_total[5m]))

# 每小时 LLM 成本（按模型）
sum by (model) (rate(cost_total[1h])) * 3600

# 当前 WebSocket 连接数
sum by (tenant_id) (websocket_connections)

# Agent 聊天成功率
sum by (action) (rate(agent_requests_total{status="success"}[5m]))
/ sum by (action) (rate(agent_requests_total[5m]))

# 缓存命中率
sum(cache_hits_total) / (sum(cache_hits_total) + sum(cache_misses_total))

# 各类型缓存命中分布
sum by (cache_type) (rate(cache_hits_total[5m]))
```

---

## 9. 关键代码位置

| 文件 | 说明 |
|---|---|
| `internal/observability/metrics.go` | Prometheus 业务指标定义（LLM / Agent / WebSocket / Cost / Cache） |
| `internal/observability/middleware.go` | HTTP 指标中间件 + OpenTelemetry Tracing 中间件 |
| `internal/observability/telemetry.go` | TracerProvider 初始化，支持 OTLP/HTTP 和 Stdout 两种导出器 |
| `internal/observability/tracer.go` | `StartSpan`、`RecordError`、`AddEvent` 等辅助函数 |
| `internal/observability/langfuse.go` | Langfuse LLM 专项追踪集成 |
| `internal/server/gin_server.go` | 注册 `/metrics` 端点与中间件（可配置关闭） |
| `internal/agent/runtime.go` | Agent 方法内埋点 + 记录 LLM 指标 |
| `internal/cost/optimizer.go` | 缓存系统 + 成本优化 |
| `internal/cost/layered_cache.go` | 多层缓存实现（精确匹配 + 语义匹配） |
| `internal/cost/embedding.go` | Embedding API 客户端（支持本地和远程） |

---

## 10. 生产环境方案选型

### 10.1 整体方案概览

| 维度 | 组件 | 开发环境（当前） | 小规模生产 | 中大规模生产 | 云托管（免运维） |
|---|---|---|---|---|---|
| **指标 Metrics** | Prometheus | Docker Compose 内置，本地盘 15 天 | Docker Compose + 挂载 SSD，15~30 天 | Thanos/VictoriaMetrics + S3，1 年+ | Grafana Cloud / AWS AMP |
| **追踪 Traces** | OpenTelemetry | `stdout` 打印到控制台 | Jaeger all-in-one，本地盘 3 天 | OTel Collector → Grafana Tempo/Jaeger + S3 | Grafana Cloud Traces / Datadog |
| **LLM 可观测** | Langfuse | 可选，Cloud 或关闭 | Langfuse Cloud | Langfuse Cloud / 自建集群 | Langfuse Cloud |
| **仪表盘** | Grafana | 可选 `docker run` | Docker Compose 内置 | Grafana 集群 | Grafana Cloud |

### 10.2 指标（Prometheus）后端方案对比

| 方案 | 存储后端 | 保留期 | 查询 | 展示 | 适用规模 | 运维成本 |
|---|---|---|---|---|---|---|
| **Prometheus 单机** | 本地 TSDB（磁盘文件） | 15~30 天 | PromQL + 自带 UI | 自带 UI / Grafana | 单实例，<100 万 active series | 低 |
| **Prometheus + Thanos** | S3/GCS/MinIO 对象存储 | 无限 | PromQL + Thanos Query | Grafana | 多集群聚合，长期保留 | 中 |
| **VictoriaMetrics 单机** | 自己写的 TSDB 引擎 | 不限，可配置 retention | PromQL + MetricsQL | Grafana | 追求高压缩率、低内存 | 低 |
| **VictoriaMetrics 集群** | 组件分离 + S3 | 无限 | PromQL + MetricsQL | Grafana | 水平扩展、多租户 | 高 |
| **Grafana Mimir** | S3/GCS 对象存储 | 无限 | PromQL | Grafana | Grafana 生态原生集成 | 高 |
| **云托管** | Grafana Cloud / AWS AMP | 可选（按套餐） | PromQL | 内置 Grafana | 不想自运维 | 无 |

### 10.3 追踪（OpenTelemetry + Traces）后端方案对比

| 方案 | 存储后端 | 保留期 | 查询方式 | 展示 UI | 适用规模 | 运维成本 |
|---|---|---|---|---|---|---|
| **Jaeger all-in-one** | 本地内存/磁盘 | 3~7 天可配置 | TraceID 搜索 + 服务名过滤 | Jaeger UI（瀑布图、火焰图） | 开发 / 小规模，< 1000 span/s | 低 |
| **Jaeger + ES/Cassandra** | Elasticsearch / Cassandra | 按存储容量 | TraceID + 标签搜索 | Jaeger UI | 中等规模，需要持久化 | 中 |
| **Grafana Tempo** | S3/GCS/MinIO 对象存储 | 无限 | TraceQL 查询语言 | Grafana（与指标同界面） | 大规模，Grafana 生态首选 | 中 |
| **SigNoz** | ClickHouse | 灵活配置 | 内置查询 | 自带 Web UI | 替代 Datadog 的开源全栈方案 | 中 |
| **Datadog APM** | 云端托管 | 按套餐 | 自然语言 + 标签 | Datadog UI | 已采购 Datadog 的团队 | 无 |
| **Grafana Cloud Traces** | Grafana 云端 | 按套餐 | TraceQL | Grafana | 不想自运维 | 无 |

### 10.4 LLM 专项可观测（Langfuse）

> Langfuse 是专门为 LLM 应用设计的开源可观测平台，原生支持 OpenTelemetry，可以无侵入地捕获每次 LLM 调用的输入/输出、Token 消耗、成本、延迟，并提供 Prompt 实验、评估和数据集管理。

| 方案 | 存储后端 | 保留期 | 核心能力 | 展示 UI | 适用场景 | 运维成本 |
|---|---|---|---|---|---|---|
| **Langfuse 自建** | PostgreSQL | 按存储容量 | LLM Trace、成本追踪、Prompt 管理、评估、数据集 | 自带 Web UI | 有运维能力的团队 | 中 |
| **Langfuse Cloud** | 云端托管 | 按套餐（免费版 50K obs/month） | 同上 + 团队协作 | 自带 Web UI | 不想自运维、快速接入 | 无 |
| **Weights & Biases** | 云端 | 按套餐 | Prompt 实验对比、Trace、微调 | 自带 Web UI | 重度 AI 实验团队 | 无 |
| **MLflow** | 自建 + S3/DB | 不限 | 实验追踪 + 模型注册（通用 ML） | 自带 Web UI | ML 全流程管理 | 中 |

**Langfuse vs Prometheus/OTel 的定位区别：**

| 维度 | Prometheus + Grafana | OpenTelemetry + Jaeger | **Langfuse** |
|---|---|---|---|
| 监控对象 | 系统指标（QPS、延迟、错误率） | 分布式链路（HTTP → Agent → LLM） | LLM 调用细节（Prompt、Response、Token、Cost） |
| 关键数据 | 数值（Counter/Gauge/Histogram） | Span 层级树 | LLM 输入/输出完整内容 |
| 典型问题 | "QPS 掉了吗？P99 多少？" | "这次请求卡在哪个环节？" | "哪个 Prompt 模板成本更低？哪个模型效果好？" |
| 适合角色 | SRE / 后端工程师 | 后端 / 全栈工程师 | AI 工程师 / Prompt 工程师 |
| 三者关系 | 互补，不互替 | 互补，不互替 | 互补，不互替 |

### 10.5 项目当前状态与推荐演进路径

| 阶段 | Metrics | Traces | LLM 专项 | 缓存 | 说明 |
|---|---|---|---|---|---|
| **当前（开发）** | ✅ Prometheus（可关闭） | ✅ stdout 打印 | 🟡 Langfuse（可关闭） | ✅ 多层缓存 | `docker-compose up` 即用 |
| **下一步（小规模生产）** | ✅ Prometheus + Grafana | ✅ Jaeger all-in-one | ✅ Langfuse Cloud | ✅ 缓存监控 | 全部跑在 docker-compose 里 |
| **中期（单机生产）** | Prometheus 挂载 SSD | Jaeger + ES 持久化 | Langfuse Cloud | ✅ 缓存优化 | 数据持久化，不再丢 |
| **长期（多实例）** | Thanos + S3 | Tempo + S3 | Langfuse Cloud | ✅ 分布式缓存 | 集群化 + 对象存储 |

---

## 11. 为什么需要 Langfuse？

### 11.1 你当前的 OTel 追踪已经能告诉你...

- 某个请求调用了哪个模型
- 花了多长时间
- 请求状态是成功还是失败

### 11.2 但 Langfuse 更进一步...

```
OTel Span（现在）          Langfuse Trace（加上后）
─────────────────────     ─────────────────────────────
model: "claude"            model: "claude"
duration: 1.2s             duration: 1.2s
status: "ok"               input_tokens: 2456
                           output_tokens: 512
                           cost: $0.032
                           prompt: "你是一个江南水乡导游..."
                           response: "您好，欢迎来到周庄！..."
                           prompt_version: v2.3
                           user_feedback: 👍
```

### 11.3 Langfuse 提供的核心能力

| 能力 | 说明 |
|------|------|
| **LLM Trace** | 完整的输入/输出内容记录 |
| **成本追踪** | 每次调用的 Token 消耗和成本估算 |
| **Prompt 管理** | 版本管理、A/B 测试、热更新 |
| **评估** | 自动化评估生成质量 |
| **数据集** | 导入真实用户对话进行测试 |

### 11.4 快速接入 Langfuse Cloud

```bash
# 1. 注册 https://cloud.langfuse.com
# 2. 获取 Public Key + Secret Key
# 3. 设置环境变量：
export LANGFUSE_PUBLIC_KEY=pk-lf-xxx
export LANGFUSE_SECRET_KEY=sk-lf-xxx

# 4. 修改配置启用 Langfuse
```

---

## 12. 相关文档

- [后端 README](../README.md)
- [多模型路由系统](MULTI_MODEL_ROUTER.md)
- [缓存系统设计](CACHE_SYSTEM.md)
- [Memory 系统设计](MEMORY_SYSTEM.md)
- [多模型配置](../MODEL_CONFIG.md)