# LLM 智能网关 详细设计说明书（开发版 v1.1）

> 本文档是《LLM 智能网关技术方案设计文档 v1.0》的增量详设，取代其中 §4.2/§4.3/§4.4/§4.5 的框架性描述。
> 阅读对象：开发工程师。目标：照此文档可直接进入编码。

---

## 0. 指标落地总表（维度 × 指标，全文以此为准）

| # | 维度 | 指标 | 算法 | Redis 结构 | Key 模式 | 默认值(示例) | 触顶动作 | 失败策略 |
|---|---|---|---|---|---|---|---|---|
| 1 | API Key | RPM | 滑动窗口(zset) | zset | `rl:k:{keyId}` | 60/min | 429 + Retry-After | fail-open |
| 2 | 租户 | RPM | 滑动窗口(zset) | zset | `rl:t:r:{tnt}` | 1000/min | 429 + Retry-After | fail-open |
| 3 | 租户 | TPM | 固定窗口计数(预扣) | string | `rl:t:tk:{tnt}` | 5M/min | 降级 L2 或 429(可配) | fail-open，挂账恢复 |
| 4 | 租户 | 并发 | 本地信号量 | chan(内存) | — | 50/租户 | 排队→503 | — |
| 5 | 模型 | 并发 | 本地信号量 | chan(内存) | — | 见容量表 §1.2 | 排队(≤2s)→fail-fast | — |
| 6 | 模型 | RPM | 令牌桶(hash) | hash | `rl:m:{model}` | 300/s 桶容 600 | 排队或 429 | fail-open |
| 7 | Provider | RPM | 固定窗口计数 | string | `rl:p:r:{pvd}` | 与上游钱包对齐×0.8 | 换 Provider | fail-open |
| 8 | Provider | TPM | 固定窗口计数(预扣) | string | `rl:p:tk:{pvd}` | 上游 TPM×0.8 | 换 Provider | 挂账恢复 |
| 9 | 全局 | 队列水位 | 本地 | chan len | — | 5000/副本 | 503 | — |

**并发为什么用本地信号量**：并发保护的是"本副本的 worker/连接资源"，每副本池大小固定（容量表 ÷ 副本数），本地信号量零 RTT、天然精确；跨副本的模型容量由 #7/#8 Provider 桶兜底。

### 0.1 指标→配置→代码可追溯

```
quota.yaml (配置中心) ──load──▶ internal/config.QuotaConfig ──▶ governance/ratelimit.Limiter
                                     │                              ├─ dims: 由 config 展开为 []Dimension
                                     │                              ├─ 每 Dimension 绑定算法(Lua 脚本/本地信号量)
                                     └─ 变更: Nacos 推送 → 热更新(10s内生效, 不需要重启)
```

---

## 1. 配置定义

### 1.1 quota.yaml（配置中心权威源）

```yaml
tenants:
  - id: tnt-a
    rpm: 1000
    tpm: 5000000
    max_concurrent: 50
    on_tpm_exceeded: degrade_l2        # degrade_l2 | reject_429 | alert_only
keys:
  - id: ak-prod-01
    tenant: tnt-a
    rpm: 300
models:
  - name: gpt-5                       # 内部统一名
    concurrency: 200                  # 全集群, 按副本数切分
    token_bucket: { rate_per_sec: 300, burst: 600 }
    providers: [ { id: pvd-openai, priority: 1 }, { id: pvd-deepseek, priority: 2 } ]
    degrade_chain: [ gpt-5-mini ]     # L1 备用 → L2 在 degrade 配置
providers:
  - id: pvd-openai
    rpm: 12000                        # 上游给的钱包 × 0.8
    tpm: 40000000
    base_url: https://api.openai.com
    adapter: openai
breaker:
  window_ms: 10000
  min_request_amount: 20
  error_ratio: 0.4
  slow_ratio: 0.5
  slow_threshold_ms: 15000
  retry_timeout_ms: 30000
  probe_count: 5
degrade:
  levels:
    L1: { type: backup_model, enabled: true }
    L2: { type: small_model, strip_tools: true, max_ctx_tokens: 4000, enabled: true }
    L3: { type: semantic_cache, min_similarity: 0.95, ttl: 3600, enabled: false }  # 首期关闭
    L4: { type: fallback_text, text: "服务繁忙,请稍后重试", enabled: true }
queue: { capacity: 5000, wait_timeout_ms: 2000, slow_client_timeout_ms: 30000 }
```

### 1.2 模型容量表（M2 压测后回填，占位示例）

| model | 单副本 concurrency | 副本数 | 全集群 in-flight | 依据 |
|---|---|---|---|---|
| gpt-5 | 10 | 20 | 200 | 推理实例压测 TTFT P99 < 3s 时的安全并发 |
| gpt-5-mini | 50 | 20 | 1000 | 同上 |

---

## 2. 代码结构

```
cmd/gateway/main.go
internal/
├── api/            # gin 路由/handler/中间件(鉴权/审计/恢复)
├── auth/           # Key 解析与元数据缓存
├── governance/
│   ├── ratelimit/  # 五维检查(pipeline)、Lua 脚本、本地信号量、本地预聚合
│   ├── quota/      # token 预扣/对账(permit 生命周期)
│   ├── breaker/    # 熔断状态机
│   └── degrade/    # 降级链执行器、续写降级、开关
├── upstream/
│   ├── adapter/    # Provider 协议适配(openai/deepseek/...)
│   ├── pool/       # 有界队列 + worker pool
│   └── stream/     # SSE 管道与背压
├── observe/        # prometheus / otel / langfuse 打点
└── config/         # 结构体 + Nacos 热更新
```

---

## 3. 核心数据结构

```go
package governance

type DimKind string

const (
    DimKey       DimKind = "key"
    DimTenantRPM DimKind = "tenant_rpm"
    DimTenantTPM DimKind = "tenant_tpm"
    DimModelConc DimKind = "model_conc"
    DimModelRPM  DimKind = "model_rpm"
    DimProvider  DimKind = "provider"
)

type Dimension struct {
    Kind   DimKind
    Key    string        // redis key 或本地信号量名
    Metric int           // 窗口阈值(RPM/TPM)或并发上限
    Window time.Duration // 0 表示非窗口类(并发/令牌桶)
}

type Permit struct {
    PermitID   string    // ULID, 对账幂等键
    Dimensions []Dimension
    Reserved   int       // 预扣 token 数
    Tenant     string
    Model      string
    CreatedAt  time.Time
}

type Deny struct {
    Dim        DimKind
    RetryAfter time.Duration
    Action     DenyAction // Reject429 | DegradeL2 | AlertOnly
}
```

---

## 4. 限流模块实现

### 4.1 Redis Lua 脚本（三个，启动时 SCRIPT LOAD 缓存 sha）

**① 滑动窗口（zset）——Key RPM / 租户 RPM**

```lua
-- KEYS[1]=rl:k:{keyId}  ARGV: window_ms, limit, now_ms
local window, limit, now = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', KEYS[1], 0, now - window)
if redis.call('ZCARD', KEYS[1]) >= limit then
    return 0
end
redis.call('ZADD', KEYS[1], now, now .. ':' .. math.random(1000000))
redis.call('PEXPIRE', KEYS[1], window * 2)
return 1
```

**② 令牌桶（hash）——模型 RPM**

```lua
-- KEYS[1]=rl:m:{model}  ARGV: capacity, rate_per_ms, now_ms
local capacity, rate, now = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
local h = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(h[1]) or capacity
local ts = tonumber(h[2]) or now
tokens = math.min(capacity, tokens + (now - ts) * rate)
if tokens < 1 then return 0 end
redis.call('HMSET', KEYS[1], 'tokens', tokens - 1, 'ts', now)
redis.call('PEXPIRE', KEYS[1], math.ceil(capacity / rate) + 1000)
return 1
```

**③ TPM 预扣（string 计数 + pending）——租户/Provider Token 预算**

```lua
-- KEYS[1]=rl:t:tk:{tnt}  KEYS[2]=pending:{permitId}
-- ARGV: window_ms, tpm_limit, need, permit_payload, now_ms
local window, limit, need = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
local used = tonumber(redis.call('GET', KEYS[1]) or '0')
if used + need > limit then
    return 0
end
redis.call('INCRBY', KEYS[1], need)
if used == 0 then redis.call('PEXPIRE', KEYS[1], window * 2) end
redis.call('SET', KEYS[2], ARGV[4], 'PX', 3600000)   -- pending 1h, 供对账/续期
return 1
```

> TPM 用固定窗口而非滑动窗口：预扣场景允许 ≤2× 窗口临界误差（换取 O(1)），误差由 reconcile 吸收，不接受则换 zset 版（按上面 ① 改造）。

### 4.2 Go 侧：五维检查一次 pipeline

```go
package ratelimit

type Limiter struct {
    rdb    *redis.ClusterClient
    shaWin, shaTB, shaTPM string // SCRIPT LOAD 结果
    sems   sync.Map               // 本地信号量: key -> chan struct{}
    agg    *LocalAggregator        // 10ms 预聚合, 见 4.3
}

// Check 五维原子检查。返回 Permit(放行) 或 Deny(含动作)。
func (l *Limiter) Check(ctx context.Context, r *Request) (*Permit, *Deny) {
    // 4/5 维(信号量除外)本地预聚合短路
    if l.agg.FastAllow(r.Tenant, r.Model) {
        return l.lazyPermit(r), nil
    }

    pipe := l.rdb.Pipeline()
    now := time.Now().UnixMilli()
    p1 := pipe.EvalSha(ctx, l.shaWin, []string{rlKey(r.KeyID)}, winMs, keyRPM, now)
    p2 := pipe.EvalSha(ctx, l.shaWin, []string{rlTenantRPM(r.Tenant)}, winMs, tRPM, now)
    p3 := pipe.EvalSha(ctx, l.shaTPM, []string{rlTenantTPM(r.Tenant), pendingKey(r.PermitID)},
        winMs, tTPM, r.ReservedTokens, r.PermitID, now)
    p4 := pipe.EvalSha(ctx, l.shaTB, []string{rlModel(r.Model)}, burst, ratePerMs, now)
    p5 := pipe.EvalSha(ctx, l.shaTPM, []string{rlPvdTPM(r.Provider), pendingKey(r.PermitID)},
        winMs, pTPM, r.ReservedTokens, r.PermitID, now)
    _, _ = pipe.Exec(ctx) // 逐条判错, 任一 err 按 fail-open 计

    for i, res := range []*redis.Cmd{p1, p2, p3, p4, p5} {
        if v, _ := res.Int(); v == 0 {
            return nil, l.buildDeny(i, r) // 映射到 Deny{Dim, Action: 配置决定}
        }
    }
    // 并发维: 本地信号量(带 ctx 排队超时)
    sem := l.semaphoreOf(r.Model, modelConc)
    if err := sem.Acquire(ctx, queueWait); err != nil {
        return nil, &Deny{Dim: DimModelConc, Action: Reject503}
    }
    l.agg.Record(r.Tenant, r.Model)
    return r.Permit, nil
}

func (l *Limiter) Release(r *Request) { l.semaphoreOf(r.Model, 0).Release() }
```

### 4.3 本地预聚合（抗 Redis 热点）

10ms 桶内同 (tenant, model) 的请求合并为一次判定：命中则放行（预聚合允许的最大突发 = 桶内阈值 × 副本数，超界再精确化）。实现：`sync.Map[tenantModel]atomic.Int32` + 10ms ticker 清零。预期将 Redis 调用量从峰值 2.5 万次/s 降到 <500 次/s。

### 4.4 本地信号量

```go
type Semaphore chan struct{}

func NewSemaphore(n int) Semaphore { return make(Semaphore, n) }

func (s Semaphore) Acquire(ctx context.Context, timeout time.Duration) error {
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    select {
    case s <- struct{}{}:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
func (s Semaphore) Release() { <-s }
```

---

## 5. 熔断模块实现

```go
package breaker

type State int32

const (
    Closed State = iota
    Open
    HalfOpen
)

type Config struct {
    Window, SlowThreshold      time.Duration
    MinRequest                 int32
    ErrorRatio, SlowRatio      float64
    Cooldown                   time.Duration
    ProbeCount                 int32
}

type bucket struct{ total, failed, slow int32 }

type Breaker struct {
    name string
    cfg  Config
    state   atomic.Int32
    mu      sync.Mutex
    ring    [10]bucket   // 1s 一个桶, 覆盖 Window=10s
    ringIdx int
    lastTripAt time.Time
    probes  atomic.Int32
}

func (b *Breaker) State() State { return State(b.state.Load()) }

func (b *Breaker) Allow() bool {
    switch b.State() {
    case Closed:
        return true
    case Open:
        if time.Since(b.lastTripAt) > b.cfg.Cooldown {
            b.mu.Lock()
            if b.state.Load() == int32(Open) { b.state.Store(int32(HalfOpen)); b.probes.Store(0) }
            b.mu.Unlock()
        }
        return false
    case HalfOpen: // 放行有限探测
        return b.probes.Add(1) <= b.cfg.ProbeCount
    }
    return true
}

// Do 执行业务调用并埋点。classify: 0=成功 1=失败 2=慢调用
func (b *Breaker) Do(ctx context.Context, fn func() error, classify func(err error, cost time.Duration) int) error {
    if !b.Allow() {
        return ErrCircuitOpen // 上游短路, 走降级链
    }
    start := time.Now()
    err := fn()
    b.record(classify(err, time.Since(start)))
    return err
}

func (b *Breaker) record(c int) {
    b.mu.Lock()
    defer b.mu.Unlock()
    if b.State() == HalfOpen {
        if c != 0 { b.state.Store(int32(Open)); b.lastTripAt = time.Now() } else if b.probes.Load() >= b.cfg.ProbeCount {
            b.state.Store(int32(Closed)); b.resetRing()
        }
        return
    }
    cur := &b.ring[b.ringIdx]
    cur.total++
    if c == 1 { cur.failed++ }
    if c == 2 { cur.slow++ }
    // 每秒滚动(由 ticker 驱动 roll), 这里仅累计; 阈值判定在 roll 时做, 见下
}

// roll 每秒调用: 前移 ring, 统计近 Window 总量并判定
func (b *Breaker) roll() {
    b.mu.Lock(); defer b.mu.Unlock()
    b.ringIdx = (b.ringIdx + 1) % len(b.ring)
    b.ring[b.ringIdx] = bucket{}
    var t, f, s int32
    for _, bk := range b.ring { t += bk.total; f += bk.failed; s += bk.slow }
    if t < b.cfg.MinRequest { return }
    if float64(f)/float64(t) > b.cfg.ErrorRatio || float64(s)/float64(t) > b.cfg.SlowRatio {
        b.state.Store(int32(Open))
        b.lastTripAt = time.Now()
    }
}
```

**埋点分类（Adapter 返回后）**：`429 → 换渠道重试，不计入`；`5xx/网络错/超时 → failed`；`cost > SlowThreshold → slow`（成功也算 slow）。

---

## 6. 降级模块实现

```go
package degrade

type Level string

const (
    L1 Level = "L1" // 备用模型
    L2 Level = "L2" // 小模型精简 prompt
    L3 Level = "L3" // 语义缓存(首期 off)
    L4 Level = "L4" // 兜底文案
)

type Executor struct {
    cfg      map[Level]LevelConf
    switches *SwitchCenter // 配置中心热更新, 支持租户/模型/比例
    caller   Caller        // 调 governance 完成一次完整调用(含重试)
}

// Exec 从指定级别开始沿链尝试, 返回结果与生效级别
func (e *Executor) Exec(ctx context.Context, req *Request, from Level) (*Response, Level, error) {
    order := []Level{L1, L2, L3, L4}
    for i, lv := range order {
        if lv < from { continue }
        if !e.switches.Enabled(lv, req.Tenant, req.Model) { continue }
        resp, err := e.tryLevel(ctx, req, lv, order[i+1:])
        if err == nil {
            metrics.DegradationTotal(lv, req.Model, req.Tenant)
            return resp, lv, nil
        }
    }
    return nil, "", ErrAllFailed
}
```

**L2 精简 prompt 规则**（确定性函数，可单测）：移除 tools 定义 → 上下文截断至 `max_ctx_tokens`（保留 system + 最近 2 轮）→ `max_tokens` 降至 min(原值, 2000) → temperature 重置为默认值。

**流式中途降级（续写）**：主模型流中断时——

1. 收集已转发 delta 序列，记 `prefix`；
2. 构造续写请求：`messages = 原 messages + {role:assistant, content: prefix} + {role:user, content:"请接着上文继续"}`
3. 发往 L1 模型，继续以 SSE 推送（对外仍是同一条流）。
已产生 token 照常计量（计费透明，避免"降级=免费"漏洞）；L1 不支持前缀续写的供应商在路由表中标记 `resume: false`，跳过。

---

## 7. SSE 转发与背压

```go
package stream

type Pipe struct {
    ch      chan Chunk      // 缓冲 256, 背压核心
    onClose func()          // 取消上游
}

type Chunk struct {
    Data   []byte
    Usage  *Usage           // 仅最后一个 chunk 携带
    IsDone bool
}

// Copy 上游→客户端, ctx 为请求 ctx(客户端断开即 Done)
func (p *Pipe) Copy(ctx context.Context, w http.ResponseWriter) error {
    flusher := w.(http.Flusher)
    for {
        select {
        case <-ctx.Done():
            p.onClose() // 取消上游模型调用, 防 goroutine/连接泄漏
            return ctx.Err()
        case c, ok := <-p.ch:
            if !ok { return nil }
            fmt.Fprintf(w, "data: %s\n\n", c.Data)
            flusher.Flush()
        }
    }
}

// 上游侧写入: 缓冲满则暂停读取上游(TCP 自然背压), slow_client_timeout 后主动断开
```

要点：网关 SSE 响应头 `Content-Type: text/event-stream` + `Cache-Control: no-cache`；结束帧 `data: [DONE]\n\n`；中断帧不向客户端发错误明文，发 `[DONE]` + 内部日志。

---

## 8. Token 计量实现

```go
package quota

// Reserve: 在 ratelimit.Check 内由 Lua③ 完成(见 4.1), 此处负责 Permit 生命周期
type Reconciler struct{ rdb *redis.ClusterClient; shaRel string }

// Consume 由 MQ 消费者驱动, 幂等
func (q *Reconciler) Consume(ctx context.Context, m UsageMsg) error {
    ok, _ := q.rdb.SetNX(ctx, "rc:"+m.PermitID, 1, 7*24*time.Hour).Result()
    if !ok { return nil } // 重复消费
    refund := m.Reserved - m.ActualTokens
    if refund > 0 {
        _, err := q.rdb.EvalSha(ctx, q.shaRel,
            []string{rlTenantTPM(m.Tenant), "pending:" + m.PermitID},
            refund).Result()
        return err
    }
    return q.rdb.Del(ctx, "pending:"+m.PermitID).Err()
}
```

```lua
-- shaRel: 回补脚本  KEYS[1]=rl:t:tk:{tnt} KEYS[2]=pending:{permitId}  ARGV[1]=refund
local used = tonumber(redis.call('GET', KEYS[1]) or '0')
local refund = math.min(tonumber(ARGV[1]), used)  -- 不回补成负数
redis.call('DECRBY', KEYS[1], refund)
redis.call('DEL', KEYS[2])
return refund
```

**usage 缺失处理**（供应商断流未返回 usage）：按已产出 delta 用 tokenizer 估算 actual；估算失败按 `Reserved × 0.6` 保守挂账，进 `reconcile_diff` 表人工/周期校准。

---

## 9. 错误码与行为矩阵

| 触发点 | 内部码 | 客户端响应 | 后续动作 |
|---|---|---|---|
| Key RPM 超限 | RL_KEY | 429 + Retry-After | 记指标 `rate_limited_total{dim=key}` |
| 租户 RPM 超限 | RL_TENANT | 429 + Retry-After | 同上 |
| 租户 TPM 预扣不足 | RL_TPM | 按配置: 429 / 静默降级 L2 | 降级记 `degradation_total{L2}` |
| 模型并发打满+排队超时 | RL_CONC | 503 + Retry-After | 记队列指标 |
| 熔断打开 | CB_OPEN | 不直接见客户端 → 走 L1 | `circuit_breaker_state=1` |
| 队列满 | QUEUE_FULL | 503 | load shedding 指标 |
| 全降级链失败 | ALL_DEGRADE_FAIL | L4 兜底文案（200, 业务字段标记 degraded=true） | Error 日志 + 告警 |
| Redis 限流故障 | RL_REDIS_ERR | 放行(fail-open) + 本地限流 | 告警, 恢复后挂账对账 |

---

## 10. 存储 DDL

```sql
CREATE TABLE llm_request_log (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  permit_id     VARCHAR(40)  NOT NULL,
  tenant_id     VARCHAR(32)  NOT NULL,
  key_id        VARCHAR(32)  NOT NULL,
  model_req     VARCHAR(64)  NOT NULL,   -- 请求模型
  model_actual  VARCHAR(64)  NOT NULL,   -- 实际执行模型(降级后不同)
  degrade_level VARCHAR(4)   DEFAULT '',
  stream        TINYINT      NOT NULL,
  input_tokens  INT          DEFAULT 0,
  output_tokens INT          DEFAULT 0,
  ttft_ms       INT          DEFAULT 0,
  total_ms      INT          DEFAULT 0,
  status_code   INT          NOT NULL,
  err_code      VARCHAR(24)  DEFAULT '',
  created_at    DATETIME(3)  NOT NULL,
  KEY idx_tenant_time (tenant_id, created_at),
  KEY idx_model_time  (model_actual, created_at)
) PARTITION BY RANGE (TO_DAYS(created_at)) (...);  -- 按天分区, 保留180天

CREATE TABLE reconcile_diff (
  id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  permit_id   VARCHAR(40) NOT NULL,
  reserved    INT NOT NULL,
  actual      INT NOT NULL,
  diff        INT NOT NULL,
  resolved    TINYINT DEFAULT 0,
  created_at  DATETIME(3) NOT NULL
);
```

---

## 11. 关键时序补充：一次请求的完整决策树（伪代码）

```go
func (h *ChatHandler) Serve(c *gin.Context) {
    req := parse(c)                                   // ① 解析+校验+裁剪
    ak := auth.Resolve(c)                             // ② Key→tenant
    est := tokenizer.Estimate(req)                    // ③ 预估 token
    permit, deny := h.limit.Check(ctx, req.WithPermit(est))
    if deny != nil {
        resp, lv, err := h.degrade.Exec(ctx, req, degradeFrom(deny.Action))
        if err == nil { return writeResp(c, resp, lv) }
        return writeDeny(c, deny)
    }
    defer h.limit.Release(req)                        // ④ 并发释放(在响应结束后)

    resp, lv, err := h.pipeline.Do(ctx, permit, req)  // ⑤ 队列→worker→熔断检查→Adapter
    // pipeline.Do 内部: 429→换provider重试(≤2,jitter); 5xx→重试(≤2); 熔断/超时→degrade.Exec(L1)
    h.publisher.Send(usageFrom(resp, permit))         // ⑥ 异步 usage→MQ→reconcile
    return streamOrPlain(c, resp, lv)                 // ⑦ SSE 或 JSON
}
```

---

## 12. 验收用例（开发自测 + QA 复用）

| # | 用例 | 方法 | 通过标准 |
|---|---|---|---|
| 1 | Key RPM 限流 | 单 key 100 QPS 打 70/min 阈值 | 第 61 个请求起 429, Retry-After 正确 |
| 2 | 窗口临界平滑 | 窗口边界突发 2×阈值 | 滑动窗口拒绝数 ≈ 阈值, 无 2× 放行 |
| 3 | TPM 预扣/回补 | 预留 10K 实际 6K | 桶值 +4K 回补; 重复消费消息幂等 |
| 4 | 多副本一致限流 | 4 副本 × 各打 300/min(key 阈值 1000) | 总量超限即拒(误差 < 本地预聚合窗) |
| 5 | 熔断状态机 | mock provider 错误率 50% | ≥20 请求后 Open; 30s 后 Half-Open; 5 探测成功 Closed |
| 6 | 慢调用熔断 | mock TTFT 20s(阈值 15s) | SlowRatio 熔断生效 |
| 7 | 429 不熔断 | mock 全 429 | 熔断器不 Open, 行为=换渠道 |
| 8 | 降级链 | kill 主模型 | L1 接管无感; kill L1 → L2(去 tools/截断可验证); 全 kill → L4 文案 |
| 9 | 流式中途降级 | 主模型流中途断 | 备用模型续写, 客户端文本连续无重复 |
| 10 | 客户端断开 | 流式响应中 kill curl | 上游 ctx 取消, goroutine 无泄漏(pprof 验证) |
| 11 | 背压 | 客户端不读, 上游快发 | 30s 后网关断开, 内存平稳 |
| 12 | Redis 宕机 | 杀 redis 主 | 请求放行(fail-open) + 告警; 恢复后对账平 |
| 13 | 配置热更新 | Nacos 改 RPM 阈值 | 10s 内生效, 不打断在途请求 |
| 14 | 压测 | 2000 QPS, 8s 均值 | 无 OOM, TTFT 增量 ≤50ms P99, Redis < 1000 QPS |

---

## 13. 开发任务拆分（建议排期）

| 任务 | 内容 | 估时 |
|---|---|---|
| T1 | 项目骨架 + 配置加载 + API/鉴权/审计 | 2d |
| T2 | SSE 转发 + 背压 + Pipe（用例 10/11） | 2d |
| T3 | 限流模块（3 Lua + pipeline + 信号量 + 预聚合，用例 1-4） | 3d |
| T4 | 熔断 + 降级链 + 续写（用例 5-9） | 4d |
| T5 | 计量对账 + MQ 消费（用例 3/12） | 2d |
| T6 | 观测打点 + 看板 + 告警 | 2d |
| T7 | 压测 + 调优（用例 14） | 3d |
