# 后端项目重构计划

## 1. 摘要

基于用户选择：**聚焦核心业务包 + 为核心包补充关键测试 + 分阶段小步提交**。

本次重构不引入新功能，目标是让代码符合 Go 社区最佳实践（Google Go Style / Effective Go），消除 P0/P1 级别的代码坏味道，提升可维护性、可读性和稳定性。重点改造 `internal/llm`、`internal/agent`、`internal/server`、`internal/cost` 四个核心包；周边包仅处理高优先级规范问题（错误处理、全局状态、命名等）。

## 2. 当前状态分析

### 2.1 已完成的改造（本次不再重复）

- `internal/agent/runtime.go`：已拆分超长函数、提取常量、`buildLLMSpanAttributes` / `markCacheHit` / `setSpanInput` / `setSpanOutput` 等辅助函数；非流式路径已较清晰。
- `internal/server/websocket_handler.go`：已拆分为 `parseConnectionPayload`、`ensurePlayer`、`streamResponse`、`persistChatResult` 等小函数，补齐 `client.SendMessage` 错误处理。
- `internal/server/gin_server.go`：`initAgentComponents` 已拆分为 `initKnowledgeBase`、`initWeatherService`、`initToolRegistry`、`initLLMAdapters`、`initEmbeddingAPI`、`initSummarizer`、`initOptimizer`、`initRuntime`、`initWebSocketHandler` 等小函数。
- `internal/cost/optimizer.go`：已消除全局变量 `summarizer` / `embeddingAPI`，`NewOptimizer` 通过构造函数注入 `Summarizer`；常量已提取；模型定价表统一保留在本包。
- `internal/llm/eino_agent_adapter.go` / `fallback_adapter.go`：已完成主要重构（类型化 context key、拆分超长函数、统一错误处理、消除重复成本计算等）。

### 2.2 实际代码状态

- 当前工作区无未提交改动（`git status` 仅 `.trae/documents/` 未跟踪）。
- 当前没有任何 `*_test.go` 文件，测试覆盖为 0。
- `internal/cost/layered_cache.go` 仍为旧版本：无优雅关闭、`cleanup` goroutine 生命周期不可控、`buildSemanticIndex` 内嵌 goroutine、`evictLRU` 实为 FIFO、`5 * time.Minute` 为魔法数字。

### 2.3 遗留问题（按优先级）

> 注：以下问题中，标有 ~~删除线~~ 的项已在前期重构中解决，本次不再重复处理。

#### P0（必须修复）

| 文件 | 问题 | 风险 | 状态 |
|------|------|------|------|
| ~~`internal/llm/eino_agent_adapter.go`~~ | ~~`getSessionIDFromContext` 使用字符串 key 且未 comma-ok~~ | ~~panic~~ | 已解决 |
| ~~`internal/agent/runtime.go`~~ | ~~`context.WithValue(ctx, "session_id", ...)` 使用字符串 key~~ | ~~命名冲突~~ | 已解决 |
| ~~`internal/llm/eino_agent_adapter.go`~~ | ~~`StreamChat` 超长函数~~ | ~~维护困难~~ | 已解决 |
| ~~`internal/cost/optimizer.go`~~ | ~~全局变量 `summarizer` / `embeddingAPI`~~ | ~~隐式依赖~~ | 已解决 |
| ~~`cmd/server/main.go`~~ | ~~`fmt.Fprintf(os.Stderr, ...)` 输出配置~~ | ~~违反日志规范~~ | 已解决 |
| ~~`internal/observability/telemetry.go`~~ | ~~`fmt.Fprintln(os.Stderr, ...)` 输出 tracer 信息~~ | ~~违反日志规范~~ | 已解决 |
| ~~`internal/llm/eino_agent_adapter.go` 与 `internal/cost/optimizer.go`~~ | ~~模型定价表重复~~ | ~~数据不一致~~ | 已解决 |
| `internal/cost/layered_cache.go` | `cleanup` goroutine 无停止机制；`buildSemanticIndex` 内嵌 goroutine | 资源泄漏、生命周期不可控 | 待修复 |
| `internal/websocket/manager.go` | `Hub.Run` 无限循环，无优雅关闭；recover 使用 `logrus` | 无法优雅关闭、日志规范不符 | 待修复 |
| `pkg/utils/panic.go` | `RecoverWithLog` 直接使用 `logrus` | 日志规范不符 | 待修复 |

#### P1（强烈建议）

| 文件 | 问题 |
|------|------|
| `internal/llm/eino_agent_adapter.go` | `Chat` 函数近 90 行，可继续拆分；`buildOutputAttributes` 同时设置 `gen_ai.request.*` 和 `gen_ai.usage.*`，语义重复；`calculateLLMCost` 与 `cost.CalculateCost` 重复 |
| `internal/server/gin_server.go:269-422` | `initAgentComponents` 约 150 行，职责过重（创建天气、工具、LLM、优化器、摘要器、Agent、WebSocket Handler） |
| `internal/llm/fallback_adapter.go` | `Chat`/`StreamChat` 重复提取 user message；流式 chunk size 为魔法数字 `10`；channel buffer 为 `10` |
| `internal/cost/layered_cache.go` | `cleanup` goroutine 无停止机制；`buildSemanticIndex` 内嵌 goroutine 生命周期不可控；`evictLRU` 实为 FIFO |
| `internal/websocket/manager.go` | `Hub.Run` 无限循环，无优雅关闭；`RecoverWithLog` 使用项目外 `logrus` |
| `pkg/utils/panic.go` | `RecoverWithLog` 直接使用 `logrus.Errorf`，应使用项目 logger 接口 |
| `internal/config/config.go` | `Load` 中环境变量处理重复代码多，可提取通用解析器 |
| `internal/emotion/detector.go` | `Detect` 中每次 `strings.ToLower` 全消息并遍历 keywords，无性能问题但可优化为 trie/预编译 |

#### P2/P3（建议）

- 全无 `_test.go`，需要为核心重构的包补充测试。
- 部分导出类型缺少文档注释。
- `internal/weather/qweather.go`、`internal/weather/openmeteo.go` 等文件本次仅做错误包装和命名修正。
- `internal/knowledge/loader.go` 中 `FAQ`、`GameRules` 等类型和函数目前未被调用，本次仅做文档化和错误处理规范化，不删除。

## 3. 改造方案

### 当前进度

| Phase | 内容 | 状态 |
|-------|------|------|
| Phase 1 | 基础安全与依赖注入（context key 类型化、消除 cost 全局状态、统一日志输出） | 已完成 |
| Phase 2 | LLM 适配器重构（消除重复定价、拆分 StreamChat/Chat、简化 FallbackAdapter） | 已完成 |
| Phase 3 | Agent Runtime 进一步清理 | 已完成 |
| Phase 4 | Server 初始化拆分 | 已完成 |
| Phase 5 | Cost & Cache 重构 | 部分完成（optimizer 已改，layered_cache 待改） |
| Phase 6 | WebSocket & Utils 清理 | 未开始 |
| Phase 7 | 周边包 P0/P1 修复 | 未开始 |
| Phase 8 | 补充单元测试 | 未开始 |

**本次执行从 Phase 5 继续推进，依次完成 Phase 5-8。**

### 总体原则

1. **保持行为不变**：所有重构只改结构和风格，不改业务语义。
2. **消除全局状态**：通过构造函数参数或选项模式注入依赖。
3. **拆分超长函数**：单个函数不超过 50 行，理想 30 行以内。
4. **统一错误处理**：`fmt.Errorf("...: %w", err)`，错误字符串小写、无结尾标点。
5. **context key 类型安全**：所有 `context.WithValue` 使用私有类型 `type ctxKey int` 常量。
6. **统一日志**：使用项目 `logging.Logger`，禁止 `fmt.Fprintf(os.Stderr)` / `logrus` 裸调用。
7. **统一模型定价**：仅保留 `cost` 包一份定价表，`llm` 包通过 `cost.CalculateCost` 计算成本。
8. **分阶段提交**：每个阶段完成后 `go build ./...`、`go vet ./...`、运行新增测试，然后提交。

---

### Phase 1：基础安全与依赖注入（先打底）

#### 3.1.1 context key 类型化

**文件**：`internal/agent/runtime.go`、`internal/llm/eino_agent_adapter.go`

- 在 `internal/agent` 包内定义：
  ```go
  type contextKey int
  const sessionIDKey contextKey = iota
  ```
- 新增导出函数：
  ```go
  func ContextWithSessionID(ctx context.Context, sessionID string) context.Context
  func SessionIDFromContext(ctx context.Context) (string, bool)
  ```
- `runtime.go` 中 `ctx = context.WithValue(ctx, "session_id", session.ID)` 改为 `agent.ContextWithSessionID(ctx, session.ID)`。
- `eino_agent_adapter.go` 中直接使用 `agent.SessionIDFromContext(ctx)`，删除 `getSessionIDFromContext`。

**规范引用**：Go Style Guide — Avoid string keys for context values。

#### 3.1.2 消除 `cost` 包全局状态

**文件**：`internal/cost/optimizer.go`、`internal/cost/summarizer_llm.go`、`internal/server/gin_server.go`

- 将 `var summarizer Summarizer` 和 `var embeddingAPI EmbeddingAPI` 删除。
- `Optimizer` 结构体直接持有 `Summarizer` 和 `EmbeddingAPI`：
  ```go
  type Optimizer struct {
      cache        *LayeredCache
      summary      *Summary
      embeddingAPI EmbeddingAPI
      summarizer   Summarizer
      mu           sync.RWMutex
  }
  ```
- `NewOptimizer` 增加 `summarizer Summarizer` 参数。
- `Summary.Add` / `calculateTotalTokens` / `compressWithLLM` 不再读取全局变量，改为使用 `s.summarizer`。
- `gin_server.go` 中创建 `LLMSummarizer` 后，直接通过 `NewOptimizer(..., summarizer)` 注入，不再调用 `cost.SetSummarizer`。
- `LayeredCache` 也不再需要全局 `embeddingAPI`。

**规范引用**：避免全局状态，使用依赖注入；Go Style Guide — Global variables。

#### 3.1.3 统一日志输出

**文件**：`cmd/server/main.go`、`internal/observability/telemetry.go`

- `main.go` 中删除 `fmt.Fprintf(os.Stderr, "[Langfuse] Config: ...")`。
- `telemetry.go` 中 `InitTracing` 增加 `logger logging.Logger` 参数，替换所有 `fmt.Fprintln(os.Stderr, ...)` 为 `logger.Info` / `logger.Warn`。
- `main.go` 调用 `observability.InitTracing(..., logger)`。

**规范引用**：project_memory — “Logs must only output to files, not console”。

---

### Phase 2：LLM 适配器重构（核心）

#### 3.2.1 删除重复定价与成本计算

**文件**：`internal/llm/eino_agent_adapter.go`、`internal/cost/optimizer.go`

- 删除 `eino_agent_adapter.go` 中的 `llmPricing` 和 `calculateLLMCost`。
- `extractUsage` 中 `Cost: calculateLLMCost(...)` 改为 `Cost: cost.CalculateCost(...)`。
- `buildOutputAttributes` 接收 `cost float64` 而非模型名，由调用方决定成本来源。
- `cost/optimizer.go` 中的 `modelPricing` 补全注释，并考虑导出 `ModelPricing` 或提供 `LookupPrice(model)`（内部保留即可）。

**注意**：`llm` 包已依赖 `cost` 包（通过 `eino_agent_adapter.go` 间接？实际当前 `llm` 包不 import `cost`，需要 import），检查是否有循环依赖：
- `cost` import `llm`（`summarizer_llm.go` 使用 `llm.Adapter`）。
- 若 `llm` import `cost`，形成 `llm -> cost -> llm` 循环依赖。
- **解决方案**：将 `ChatUsage.Cost` 的计算上移到调用方（`runtime.go` 已调用 `cost.CalculateCost`），`llm` 包只负责透传 token，不负责成本。`extractUsage` 不再填充 `Cost`，`buildOutputAttributes` 只设置 token 属性，不设置 `GenAIUsageCost`；由 `runtime.go` 统一设置成本属性。

#### 3.2.2 拆分 `StreamChat`

**文件**：`internal/llm/eino_agent_adapter.go`

目标：将 420 行的 `StreamChat` 拆分为职责单一的小函数，消除重复 select 模式。

- 新增类型：
  ```go
  type streamState struct {
      modelName        string
      usage            *ChatUsage
      finalMsg         *eino_schema.Message
      isFirstModelCall bool
      toolCallSpan     trace.Span
      secondModelSpan  trace.Span
      secondModelOut   strings.Builder
      secondModelFinal *eino_schema.Message
  }
  ```
- 新增方法：
  ```go
  func (a *EinoAgentAdapter) runStream(ctx context.Context, iter adkIterator, out chan<- *StreamResult, done <-chan struct{}) // 主循环
  func (s *streamState) handleAssistantMessage(ctx context.Context, msg *eino_schema.Message, out chan<- *StreamResult) error
  func (s *streamState) handleStreamingMessage(ctx context.Context, stream *eino_schema.MessageStream, out chan<- *StreamResult) error
  func (s *streamState) handleToolCalls(ctx context.Context, tcs []eino_schema.ToolCall) // 创建 tool span
  func (s *streamState) emitContentChunk(ctx context.Context, content, model string, out chan<- *StreamResult) error
  func (s *streamState) emitReasoningChunk(ctx context.Context, reasoning, model string, out chan<- *StreamResult) error
  func (s *streamState) closeSpans()
  ```
- 主循环逻辑：
  1. `iter.Next()` 读取事件；
  2. 如果是 Assistant 非流式消息，走 `handleAssistantMessage`；
  3. 如果是流式消息，走 `handleStreamingMessage`；
  4. 迭代结束后统一 emit exit 事件、关闭 spans、记录 stats。
- 消除每个 select 分支中重复的 `stream.Close()` / `span.End()` 代码，使用 `defer s.closeSpans()` 或独立 cleanup 函数。
- channel buffer 改为命名常量 `streamResultBuffer = 100`（业务上允许，因为规则是“要么是 1，要么是无缓冲”，但此处生产者/消费者速率不匹配，保持 100 是实际选择；若严格遵循规则，可改为 1 并依赖流式背压。这里保留 100 并加注释说明这是有界背压队列）。

**规范引用**：函数长度、单一职责、消除重复代码、goroutine 生命周期清晰。

#### 3.2.3 拆分 `Chat`

**文件**：`internal/llm/eino_agent_adapter.go`

- 提取 `buildRunOptions(opts ...ChatOption)`：根据选项生成 `eino_adk.AgentRunOption`。
- 提取 `runADK(ctx, messages, runOpts)`：执行 runner 并返回最终消息/错误。
- 提取 `recordChatStats(...)`。
- `Chat` 主函数保持约 30 行：校验 → 构建选项 → 创建 span → 执行 → 处理结果 → 设置属性。

#### 3.2.4 简化 `FallbackAdapter`

**文件**：`internal/llm/fallback_adapter.go`

- 提取 `extractUserContent(messages)`，消除 `Chat` 和 `StreamChat` 中的重复循环。
- 流式 chunk size 提取常量 `fallbackChunkSize = 10`。
- channel buffer 提取常量 `fallbackStreamBuffer = 10`。
- `matchResponse` 中的 keywords 提取为包级常量/变量，避免每次调用重建 map（虽然 Go 编译器会优化，但显式更好）。

---

### Phase 3：Agent Runtime 进一步清理

**文件**：`internal/agent/runtime.go`

当前已较好，仍有以下微调：

- `HandleWelcome` 中 `notifyChan := make(chan struct{})` 未缓冲，后续 `close(notifyChan)` 会唤醒等待者，符合规则。
- `handleWelcomeInternal` 的 `startTime` 仅在成功路径使用，失败路径中 `handleWelcomeError` 也用到，OK。
- `processLLMResponse` 中的 `startTime := time.Now()` 在函数开头，实际只在最后计算 latency，建议改名为 `recordStart` 或重构成 `recordChatMetrics` 接收 `startTime`。
- 将 `defaultModelName = "unknown"` 也用于 `recordWelcomeMetrics` 和 `updateLLMStatsAndMetrics` 中的 `model == ""` 判断，改为调用 `normalizeModelName(model)` 辅助函数。
- `buildContextMessages` 已较好，保持不变。

---

### Phase 4：Server 初始化拆分

**文件**：`internal/server/gin_server.go`

- 将 `initAgentComponents` 拆分为多个纯函数/方法：
  ```go
  func (s *Server) initKnowledgeBase(kb interface{}) (*knowledge.KnowledgeBase, error)
  func (s *Server) initWeatherService() (weather.Service, error)
  func (s *Server) initToolRegistry(kb *knowledge.KnowledgeBase, weatherSvc weather.Service) (*tools.ToolRegistry, error)
  func (s *Server) initLLMAdapter(registry *tools.ToolRegistry) (llm.Adapter, *llm.FallbackAdapter)
  func (s *Server) initEmbeddingAPI() cost.EmbeddingAPI
  func (s *Server) initSummarizer(adapter llm.Adapter) cost.Summarizer
  func (s *Server) initOptimizer(embedding cost.EmbeddingAPI, summarizer cost.Summarizer) *cost.Optimizer
  func (s *Server) initRuntime(adapter, fallback llm.Adapter, registry *tools.ToolRegistry, optimizer *cost.Optimizer, detector emotion.Detector) *agent.Runtime
  func (s *Server) initWebSocketHandler(runtime *agent.Runtime) *WebSocketHandler
  ```
- 每个 helper 负责单一组件创建，错误直接返回。
- 删除 `s.config.Cost.Embedding.Enabled` 分支中的空 `else {}`。

---

### Phase 5：Cost & Cache 重构

#### 3.5.1 `optimizer.go`

- `modelPricing` 注释补全，明确单位是 USD / 1K tokens。
- `CalculateCost` 中默认 fallback 模型改为常量 `defaultPricingModel = "gpt-3.5-turbo"`。
- `NewOptimizer` 增加 `summarizer` 参数后，调用 `NewSummary(maxMessages, tokenLimit, summarizer)`，把 `Summarizer` 传入 `Summary`。
- `Summary` 结构体持有 `summarizer Summarizer`，`compressWithLLM` 和 `calculateTotalTokens` 使用它。
- `compress` 中保留最近消息数 `3` 提取常量 `fallbackRecentMessages = 3`。
- `compressWithLLM` 中 `recentCount := 2` 提取常量 `llmSummaryRecentMessages = 2`。

#### 3.5.2 `layered_cache.go`

- 给 `LayeredCache` 增加 `stopCleanup chan struct{}` 和 `cleanupWg sync.WaitGroup`，提供 `Stop()` 方法用于优雅关闭。
- `NewLayeredCache` 启动 cleanup goroutine，并注册到 `cleanupWg`。
- `buildSemanticIndex` 改为同步调用（由调用方决定是否 go），避免不可控 goroutine；或在调用处使用 `go c.buildSemanticIndex(...)` 并加 recover，但不再把 `go` 藏在 `Set` 内部。
- `evictLRU` 改名为 `evictOldest`（实际逻辑是 FIFO），或实现真正的 LRU（使用 container/list）。考虑到简单性，先改名为 `evictOldest` 并加注释。
- `cleanup` 中的 ticker 间隔 `5 * time.Minute` 提取常量 `cacheCleanupInterval`。

---

### Phase 6：WebSocket & Utils 清理

#### 3.6.1 `internal/websocket/manager.go`

- `Hub` 增加 `stop chan struct{}` 和 `wg sync.WaitGroup`。
- 新增 `Run()` 内 select 监听 `h.stop`，收到后退出循环并等待处理完当前事件。
- 新增 `Stop()` 方法关闭 stop channel 并等待 goroutine 退出。
- `RecoverWithLog` 当前使用 `logrus.Errorf`，但 `Hub.Run` 中已使用 `utils.RecoverWithLog`；该问题在 Phase 6.2 中统一处理：改为可注入 logger 的 recover，或至少让 Hub 使用 `RecoverWithCustomLogger`。

#### 3.6.2 `pkg/utils/panic.go`

- 删除 `RecoverWithLog`（直接使用 logrus）。
- 所有调用点改为 `RecoverWithCustomLogger(component, logger)`。
- 检查 `internal/websocket/manager.go` 中 `defer utils.RecoverWithLog("Hub.UnregisterClient")` 改为 `RecoverWithCustomLogger`。

#### 3.6.3 `pkg/utils/retry.go`

- 当前实现已较规范，保持不变，后续加测试。

---

### Phase 7：周边包 P0/P1 修复

#### 3.7.1 `internal/config/config.go`

- 环境变量 `${...}` 解析逻辑重复 4 次，提取通用函数：
  ```go
  func resolveEnvVar(value, defaultName string) string
  ```
- `Load` 函数拆分：
  ```go
  func loadFromFile(path string) (*Config, error)
  func (cfg *Config) resolveModelAPIKeys()
  func (cfg *Config) resolveLangfuseKeys()
  func (cfg *Config) resolveDatabase()
  func (cfg *Config) resolveObservability()
  func (cfg *Config) resolveWeatherAndEmbedding()
  ```

#### 3.7.2 `internal/emotion/detector.go`

- 将 `strings.ToLower(message)` 改为直接匹配中文字符串（中文大小写不敏感，且 keyword 都是中文/小写，可省一次转换）。
- 规则 keywords 保持不变，添加 `Detect` 的单元测试。

#### 3.7.3 `internal/knowledge/loader.go`

- 为导出类型和函数补齐文档注释（以类型/函数名开头）。
- 错误统一使用 `fmt.Errorf("...: %w", err)`。
- 加 `Load` / `FindQuestion` 单元测试（使用临时文件）。

#### 3.7.4 `internal/database/*_repo.go`

- 补齐错误包装：`return nil, fmt.Errorf("get player by id %s: %w", id, err)`。
- 补齐文档注释。

#### 3.7.5 `internal/weather/qweather.go`、`openmeteo.go`

- 补齐错误包装，消除魔法数字（超时、重试次数等使用常量）。
- 不改动业务逻辑。

---

### Phase 8：补充单元测试

为核心重构的包添加表驱动测试，使用标准库 `testing`，不使用断言库。

| 测试文件 | 覆盖内容 |
|----------|----------|
| `internal/cost/optimizer_test.go` | `CalculateCost` 已知模型、未知模型 fallback、零 token；`Summary.Add` / `Get` / `compress`； |
| `internal/cost/embedding_test.go` | `cosineSimilarity` 同向量=1、正交=0、零向量=0；`LocalEmbeddingClient.generatePseudoEmbedding` 维度正确； |
| `internal/llm/fallback_adapter_test.go` | `Chat` 返回默认回复；`matchResponse` 关键词命中与 miss；`StreamChat` 能按顺序吐出 chunk 和 exit； |
| `internal/emotion/detector_test.go` | 各情绪关键词命中、neutral 默认； |
| `internal/knowledge/loader_test.go` | 临时 JSON 文件加载、`FindQuestion`、`FindByTag`； |
| `pkg/utils/retry_test.go` | 成功不重试、失败达到最大重试次数、context 取消提前退出、指数退避； |
| `internal/agent/runtime_test.go` | `buildContextMessages` 空历史、短历史、长历史摘要分支（mock summarizer）；`ContextWithSessionID` / `SessionIDFromContext`； |

测试规范：
- 表驱动 + `t.Run` 子测试。
- 失败信息：`YourFunc(%v) = %v, want %v`。
- 辅助函数调用 `t.Helper()`。
- 比较结构体用 `cmp.Diff`（若已引入 `github.com/google/go-cmp/cmp`，否则用 reflect.DeepEqual + 自定义错误信息）。

---

## 4. 假设与决策

1. **不引入新依赖**：除测试可能使用 `github.com/google/go-cmp/cmp`（若已在 go.mod 中）外，不引入新库。优先使用标准库。
2. **不改变 Langfuse / OTel 语义**：所有 span 属性名、事件名保持与当前一致，避免破坏可观测性。
3. **不删除未使用导出 API**：如 `knowledge.GetFAQ`、`GetGameRules` 等，仅规范化和加注释，避免破坏潜在调用方。
4. **循环依赖处理**：`llm` 包不再直接计算成本，由调用方 `runtime` 统一设置成本属性，从而避免 `llm -> cost -> llm` 循环。
5. **channel buffer**：流式 channel buffer 保持 100（有界背压），其余内部通知 channel 使用无缓冲或 1。
6. **并发模型**：`Hub.Run` 增加优雅关闭；`LayeredCache.cleanup` 可停止；异步索引构建显式由调用方 `go` 启动。

## 5. 验证步骤

每个 Phase 完成后执行：

1. `cd /Users/ninglg/taohuawu/backend && gofmt -w .`
2. `goimports -w .`（确保使用最新版 goimports）
3. `go build ./...`
4. `go vet ./...`
5. `go test ./...`（运行该阶段新增的测试）
6. 若有失败，修复后再进入下一阶段。

全部 Phase 完成后：

1. 端到端启动服务（若环境允许），验证 WebSocket 连接、聊天、欢迎语正常。
2. 检查日志只输出到文件/控制台是否还有违规的 `fmt.Fprintf(os.Stderr, ...)`：
   ```bash
   rg "fmt\.Fprint.*os\.Stderr|fmt\.Fprintln.*os\.Stderr|logrus\." --type go
   ```
3. 检查是否还有 `context.WithValue(ctx, "session_id"` 等字符串 key：
   ```bash
   rg "WithValue\(ctx, \"" --type go
   ```
4. 检查全局 `var summarizer`、`var embeddingAPI` 是否已删除：
   ```bash
   rg "^var (summarizer|embeddingAPI)" --type go
   ```

## 6. 提交计划

每完成一个 Phase 提交一次，提交信息格式：

```
refactor(backend): <phase 简述>

- <改动点 1>
- <改动点 2>
```

例如：
- `refactor(backend): phase 1 类型化 context key 并消除 cost 全局状态`
- `refactor(backend): phase 2 重构 LLM 适配器，拆分 StreamChat 与 Chat`
- `refactor(backend): phase 3 清理 runtime 辅助函数与常量`
- `refactor(backend): phase 4 拆分 server agent 初始化`
- `refactor(backend): phase 5 重构 cost optimizer 与 layered cache`
- `refactor(backend): phase 6 优化 websocket hub 与 panic 恢复日志`
- `refactor(backend): phase 7 清理 config/emotion/knowledge/database/weather`
- `test(backend): phase 8 为核心包补充单元测试`

最终全部完成后，再向用户汇报总览。
