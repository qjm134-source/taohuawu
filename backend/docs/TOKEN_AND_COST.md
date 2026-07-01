# Token 计算与成本统计

本文档详细说明江南水乡智能导游系统的 Token 获取、估算、压缩以及成本统计的全链路方案。

---

## 目录

- [1. 架构概览](#1-架构概览)
- [2. Token 获取](#2-token-获取)
- [3. Token 估算](#3-token-估算)
- [4. Token 压缩（成本优化）](#4-token-压缩)
- [5. 成本计算](#5-成本计算)
- [6. 指标统计与上报](#6-指标统计与上报)
- [7. 完整数据流](#7-完整数据流)
- [8. 相关代码索引](#8-相关代码索引)

---

## 1. 架构概览

```
用户消息 → HandleChat / HandleChatStream
    │
    ├─ 缓存检查（精确匹配 / 语义匹配）→ 命中则跳过 LLM，Token = 0
    │
    ├─ buildContextMessages()
    │   ├─ EstimateTokens() → 估算历史 Token 数
    │   └─ Token > 4096 阈值 → 触发 LLM 摘要压缩
    │
    ├─ LLM 调用（EinoAgentAdapter.Chat / StreamChat）
    │   └─ extractUsage() → 从 ResponseMeta.Usage 提取真实 Token
    │
    ├─ CalculateCost(model, inputTokens, outputTokens)
    │   └─ 模型定价表 × (tokens/1000) → 成本
    │
    └─ 多路指标上报:
        ├─ Prometheus: llm_request_tokens / llm_completion_tokens / cost_total
        ├─ Langfuse: RecordGeneration(inputTokens, outputTokens, cost)
        ├─ 数据库: conversations.llm_tokens / conversations.cost
        └─ WebSocket: NPCReplyChunkPayload.TotalTokens / Cost
```

---

## 2. Token 获取

### 2.1 数据结构

在 `internal/llm/adapter.go` 中定义了统一的 Token 使用量结构：

```go
type ChatUsage struct {
    PromptTokens     int    // 输入 Token
    CompletionTokens int    // 输出 Token
    TotalTokens      int    // 总 Token
    Model            string // 模型名称
}
```

流式和非流式响应均通过此结构传递 Token 数据。`StreamChunk` 中嵌入 `Usage ChatUsage`，流结束时汇总统计。

### 2.2 从 LLM 响应提取 Token

核心提取逻辑在 `internal/llm/eino_agent_adapter.go` 的 `extractUsage()` 方法：

```go
func (a *EinoAgentAdapter) extractUsage(msg *eino_schema.Message) ChatUsage {
    if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
        return ChatUsage{Model: a.getCurrentModelName(msg)}
    }
    return ChatUsage{
        PromptTokens:     int(msg.ResponseMeta.Usage.PromptTokens),
        CompletionTokens: int(msg.ResponseMeta.Usage.CompletionTokens),
        TotalTokens:      int(msg.ResponseMeta.Usage.TotalTokens),
        Model:            a.getCurrentModelName(msg),
    }
}
```

**关键点**：
- Token 数据直接来自 LLM API 响应中的 `usage` 字段（由 Eino 框架自动解析到 `ResponseMeta.Usage`）
- 模型名称优先从消息 `Extra["model_name"]` 获取，回退到配置中的主模型名
- 非流式调用（`Chat`）和流式调用（`StreamChat`）均通过同一个 `extractUsage()` 提取

### 2.3 非流式调用中的 Token 记录

在 `internal/agent/runtime.go` 的 `HandleChat()` 中：

```go
stats.Model        = usage.Model
stats.InputTokens  = usage.PromptTokens
stats.OutputTokens = usage.CompletionTokens
stats.TotalTokens  = usage.TotalTokens
stats.Cost = cost.CalculateCost(usage.Model, usage.PromptTokens, usage.CompletionTokens)
```

### 2.4 流式调用中的 Token 记录

在 `internal/agent/runtime.go` 的 `HandleChatStream()` 中，Token 信息在流结束时获取：

```go
// Usage 信息可能在最后一个 chunk 返回
if chunk.Usage.TotalTokens > 0 {
    stats.InputTokens  = chunk.Usage.PromptTokens
    stats.OutputTokens = chunk.Usage.CompletionTokens
    stats.TotalTokens  = chunk.Usage.TotalTokens
}
```

流结束后的汇总：

```go
stats.LatencyMs = time.Since(startTime).Milliseconds()
stats.Cost = cost.CalculateCost(stats.Model, stats.InputTokens, stats.OutputTokens)
```

---

## 3. Token 估算

在实际调用 LLM 之前，需要对历史消息的 Token 数做本地估算，用于判断是否需要压缩上下文。

### 3.1 估计算法

实现位于 `internal/cost/summarizer_llm.go` 的 `EstimateTokens()` 方法：

```go
func (s *LLMSummarizer) EstimateTokens(text string) int {
    tokens := 0
    for _, r := range text {
        if r >= 0x4E00 && r <= 0x9FFF {
            tokens += 2          // 中文字符 → 2 token
        } else if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
            continue             // 英文字母 → 跳过，后续按单词计算
        } else if r == ' ' || r == '\t' || r == '\n' {
            continue             // 空白字符 → 跳过
        } else {
            tokens++             // 其他字符 → 1 token
        }
    }
    // 英文单词 → 1.3 token/词
    words := strings.Fields(text)
    englishWordCount := 0
    for _, word := range words {
        for _, r := range word {
            if utf8.RuneLen(r) == 1 && ((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
                englishWordCount++
                break
            }
        }
    }
    tokens += int(float64(englishWordCount) * 1.3)
    if tokens < 1 {
        tokens = 1
    }
    return tokens
}
```

**规则总结**：

| 字符类型 | 估算 Token 数 |
|---------|-------------|
| 中文字符（U+4E00 ~ U+9FFF） | 2 |
| 英文单词 | 1.3 |
| 空白字符 | 0 |
| 其他字符（标点、数字、符号等） | 1 |

> **注意**：这是一个本地轻量估算，不等同于 LLM 的真实 Token 计数，仅用于触发上下文压缩的预判。

### 3.2 使用场景

在 `internal/agent/runtime.go` 的 `buildContextMessages()` 中，用来判断历史消息是否超过阈值：

```go
const tokenThreshold = 4096

// 累加历史消息的预估 Token
totalTokens := 0
for _, msg := range allMessages {
    if summarizer != nil {
        totalTokens += summarizer.EstimateTokens(msg.Content)
    } else {
        totalTokens += len([]rune(msg.Content)) * 2  // 降级估算法
    }
}
totalTokens += summarizer.EstimateTokens(currentMessage)

// 超过阈值则触发压缩
if totalTokens > tokenThreshold {
    // ... 执行 LLM 摘要压缩
}
```

---

## 4. Token 压缩（成本优化）

### 4.1 触发条件

在 `internal/cost/optimizer.go` 的 `Summary.Add()` 中：

```go
func (s *Summary) Add(role, content string) {
    s.history = append(s.history, Message{Role: role, Content: content})

    // 方式一：Token 数量超限 → LLM 增量摘要压缩
    if s.tokenLimit > 0 && summarizer != nil {
        totalTokens := s.calculateTotalTokens()
        if totalTokens > s.tokenLimit {
            s.compressWithLLM()
            return
        }
    }

    // 方式二：消息数量超限 → 简单压缩
    if len(s.history) >= s.maxMessages {
        s.compress()
    }
}
```

### 4.2 增量式 LLM 摘要压缩

不是简单截断，而是用 LLM 将早期对话"理解并总结"成一段短文：

```go
func (s *Summary) compressWithLLM() {
    recentMessages := s.history[len(s.history)-2:]        // 保留最近 2 条
    messagesToSummarize := s.history[:len(s.history)-2]    // 压缩旧消息

    textToSummarize := s.messagesToText(messagesToSummarize)

    // 增量式：如果已有摘要，在旧摘要基础上追加
    if s.currentSummary != "" {
        newSummary = summarizer.IncrementalSummarize(ctx, s.currentSummary, textToSummarize)
    } else {
        newSummary = summarizer.Summarize(ctx, textToSummarize)
    }

    // 构建新历史：摘要 + 最近消息
    s.history = append([]Message{{Role: "system", Content: "[对话摘要] " + newSummary}}, recentMessages...)
}
```

### 4.3 buildContextMessages 中的压缩

`internal/agent/runtime.go` 的 `buildContextMessages()` 也有一套独立的上下文压缩逻辑：

- 阈值：4096 Token
- 超过阈值时，保留最近 6 条原始消息，早期消息交给 LLM 做增量式摘要
- 摘要失败时降级为简单文本提示：`"[之前有N条对话，以下是最近的对话内容]"`

### 4.4 其他 Token 节省手段

| 手段 | 说明 |
|------|------|
| **精确匹配缓存** | SHA256(question:model) 精确匹配，命中则跳过 LLM，Token 消耗为 0 |
| **语义匹配缓存** | Embedding 向量 + 余弦相似度，相似度 > 0.85 时返回缓存答案 |
| **工具结果缓存** | SHA256(tool_name:params) 缓存外部 API 调用结果 |
| **成本优先路由** | 启用 `StrategyCost` 策略，自动选单价最低的模型 |

---

## 5. 成本计算

### 5.1 模型定价表

定义在 `internal/cost/optimizer.go`，当前支持的 12 款模型定价（每 1K tokens，单位：美元）：

| 模型 | 输入价格 | 输出价格 | 厂商 |
|------|---------|---------|------|
| `deepseek-chat` | $0.0001 | $0.0002 | DeepSeek |
| `deepseek-reasoner` | $0.00055 | $0.00219 | DeepSeek |
| `gpt-4o` | $0.0025 | $0.01 | OpenAI |
| `gpt-4o-mini` | $0.00015 | $0.0006 | OpenAI |
| `gpt-3.5-turbo` | $0.0005 | $0.0015 | OpenAI |
| `claude-3-5-sonnet` | $0.003 | $0.015 | Anthropic |
| `claude-3-haiku` | $0.00025 | $0.00125 | Anthropic |
| `qwen-turbo` | $0.0002 | $0.0006 | 阿里云 |
| `qwen-plus` | $0.0004 | $0.0012 | 阿里云 |
| `qwen-max` | $0.002 | $0.006 | 阿里云 |
| `doubao-pro-32k` | $0.0008 | $0.005 | 字节跳动 |
| `doubao-pro-128k` | $0.005 | $0.009 | 字节跳动 |
| `glm-4` | $0.01 | $0.01 | 智谱 |
| `glm-4-flash` | $0.0001 | $0.0001 | 智谱 |

### 5.2 计算函数

```go
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
    pricing, ok := modelPricing[model]
    if !ok {
        pricing = modelPricing["gpt-3.5-turbo"] // 默认价格
    }
    inputCost  := float64(inputTokens)  / 1000 * pricing.Input
    outputCost := float64(outputTokens) / 1000 * pricing.Output
    return inputCost + outputCost
}
```

公式：`成本 = (输入Token / 1000 × 输入单价) + (输出Token / 1000 × 输出单价)`

对于未匹配到的模型，使用 `gpt-3.5-turbo` 价格作为默认估算。

---

## 6. 指标统计与上报

### 6.1 Prometheus 指标

定义在 `internal/observability/metrics.go`：

| 指标名称 | 类型 | 标签 | 说明 |
|---------|------|------|------|
| `llm_requests_total` | Counter | `model`, `status` | LLM 请求总数 |
| `llm_request_duration_seconds` | Histogram | `model` | LLM 请求耗时分布 |
| `llm_request_tokens_total` | Counter | `model` | 请求 Token 总量（输入） |
| `llm_completion_tokens_total` | Counter | `model` | 完成 Token 总量（输出） |
| `cost_total` | Counter | `model` | LLM 调用总成本（$） |
| `cache_hits_total` | Counter | `cache_type` | 缓存命中次数 |
| `cache_misses_total` | Counter | `cache_type` | 缓存未命中次数 |
| `cache_hit_ratio` | Gauge | - | 缓存命中率 |

上报入口在 `internal/agent/runtime.go` 的 `recordLLMMetrics()`：

```go
func (r *Runtime) recordLLMMetrics(model, status string, durationSec float64,
    inputTokens, outputTokens int, costAmount float64) {
    observability.LLMRequestsTotal.WithLabelValues(model, status).Inc()
    observability.LLMRequestDuration.WithLabelValues(model).Observe(durationSec)
    observability.LLMRequestTokens.WithLabelValues(model).Add(float64(inputTokens))
    observability.LLMCompletionTokens.WithLabelValues(model).Add(float64(outputTokens))
    observability.CostTotal.WithLabelValues(model).Add(costAmount)
}
```

查看地址：`http://localhost:8080/metrics`

### 6.2 Langfuse 专门追踪

在 `internal/observability/langfuse.go` 的 `RecordGeneration()` 中：

```go
func (t *LLMTrace) RecordGeneration(name, model string,
    input interface{}, output string,
    inputTokens, outputTokens int,
    cost float64, latencyMs int64, err error) {

    usage := &langfuse.Usage{
        Input:  inputTokens,
        Output: outputTokens,
        Total:  inputTokens + outputTokens,
        Unit:   "TOKENS",
    }
    // ... 同时记录 cost、latency_ms、input_tokens、output_tokens 到 metadata
}
```

### 6.3 数据库持久化

每次对话结束后，Token 和成本写入 `conversations` 表：

| 字段 | 类型 | 说明 |
|------|------|------|
| `llm_tokens` | INT | LLM Token 消耗数 |
| `cost` | DECIMAL(10,6) | LLM 调用成本（美元） |
| `llm_model` | VARCHAR | 使用的模型名称 |
| `cache_hit` | BOOLEAN | 是否命中缓存 |

### 6.4 WebSocket 前端推送

流式响应结束后，通过 `NPCReplyChunkPayload` 将统计信息推送给前端：

```go
type NPCReplyChunkPayload struct {
    // ...
    InputTokens  int     `json:"inputTokens"`
    OutputTokens int     `json:"outputTokens"`
    TotalTokens  int     `json:"totalTokens"`
    Cost         float64 `json:"cost"`
    LatencyMs    int64   `json:"latencyMs"`
}
```

---

## 7. 完整数据流

### 7.1 非流式调用

```
HandleChat()
  → buildContextMessages()
    → EstimateTokens() 估算历史 Token
    → Token > 4096 ? LLM 摘要压缩 : 直接使用
  → adapter.Chat() → LLM API 调用
  → extractUsage() 提取 PromptTokens / CompletionTokens / TotalTokens
  → CalculateCost(model, inputTokens, outputTokens)
  → recordLLMMetrics() 上报 Prometheus
  → langfuseTrace.RecordGeneration() 上报 Langfuse
  → 数据库持久化
```

### 7.2 流式调用

```
HandleChatStream()
  → buildContextMessages() 同上
  → adapter.StreamChat()
  → 循环 Recv() 接收 StreamChunk
    → chunk.Usage.TotalTokens > 0 时记录 Token 信息
  → 流结束后:
    → CalculateCost(model, inputTokens, outputTokens)
    → 上报 Prometheus / Langfuse / DB / WebSocket
```

### 7.3 缓存命中流程

```
HandleChat() / HandleChatStream()
  → optimizer.GetCache(question)  → 精确匹配
  → optimizer.CheckSimilarity()   → 语义匹配
    → 命中: 直接返回缓存答案
      → Token = 0, Cost = 0
      → 仅上报 AgentRequestsTotal(success)
      → 不上报 LLM Token/Cost 指标
```

---

## 8. 相关代码索引

| 模块 | 文件 | 关键内容 |
|------|------|---------|
| 适配器接口 | `internal/llm/adapter.go` | `ChatUsage` 结构体、`Adapter` 接口 |
| Token 提取 | `internal/llm/eino_agent_adapter.go` | `extractUsage()`、`getCurrentModelName()` |
| 模型定价 | `internal/cost/optimizer.go` | `modelPricing`、`CalculateCost()` |
| 摘要压缩 | `internal/cost/optimizer.go` | `Summary.Add()`、`compressWithLLM()` |
| Token 估算 | `internal/cost/summarizer_llm.go` | `EstimateTokens()` |
| 上下文构建 | `internal/agent/runtime.go` | `buildContextMessages()`、`tokenThreshold=4096` |
| 会话管理 | `internal/agent/runtime.go` | `HandleChat()`、`HandleChatStream()` |
| 指标定义 | `internal/observability/metrics.go` | Prometheus Token/Cost 指标 |
| 指标上报 | `internal/agent/runtime.go` | `recordLLMMetrics()` |
| Langfuse 追踪 | `internal/observability/langfuse.go` | `RecordGeneration()` |
| 数据库模型 | `internal/database/models.go` | `Conversation.LLMTokens`、`Conversation.Cost` |
| 前端推送 | `internal/websocket/message.go` | `NPCReplyChunkPayload.TotalTokens/Cost` |
| 服务初始化 | `internal/server/gin_server.go` | `initAgentComponents()` → 初始化 Optimizer |

---

## 如何扩展

### 添加新模型的定价

在 `internal/cost/optimizer.go` 的 `modelPricing` map 中添加：

```go
var modelPricing = map[string]struct {
    Input  float64
    Output float64
}{
    // ... 已有定价 ...
    "new-model-name": {Input: 0.002, Output: 0.008},
}
```

### 调整压缩阈值

修改 `internal/agent/runtime.go` 中的常量：

```go
const tokenThreshold = 4096  // 调整为更大的值以允许更多上下文
```
