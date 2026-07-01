# 多模型路由系统

## 架构概述

多模型路由系统基于 **Eino 框架**构建，提供统一的 LLM 服务层。通过 Eino 的 `ReAct Agent` 实现模型调用，并在 `ToolCallingModel` 层通过 `failoverChatModel` 包装器实现模型级故障转移，所有模型统一通过 OpenAI 兼容接口接入。

### 核心组件

```
┌─────────────────────────────────────────────────────────┐
│                    Application Layer                     │
│              (agent.Runtime, websocket, etc.)            │
└────────────────────┬────────────────────────────────────┘
                     │
                     │ llm.Adapter 接口
                     │
┌────────────────────▼────────────────────────────────────┐
│                  EinoAdapter                             │
│        (适配项目接口到 Eino ChatModelAgent)               │
└────────────────────┬────────────────────────────────────┘
                     │
                     │ eino_schema.Message
                     │
┌────────────────────▼────────────────────────────────────┐
│              Eino ReAct Agent                            │
│  ┌─────────────────────────────────────────────────┐   │
│  │  ToolCallingModel = failoverChatModel            │   │
│  │  (模型级故障转移包装器)                           │   │
│  │  • MaxRetries - 最大故障转移尝试次数              │   │
│  │  • ShouldFailover - 判断是否需要降级              │   │
│  │  • GetFailoverModel - 选择降级模型                │   │
│  └─────────────────────────────────────────────────┘   │
│  ┌─────────────────────────────────────────────────┐   │
│  │  ToolsNode (工具调用)                            │   │
│  └─────────────────────────────────────────────────┘   │
│  ┌─────────────────────────────────────────────────┐   │
│  │  路由策略引擎 (Strategy Engine)                  │   │
│  │  • Fixed      - 固定使用指定模型                   │   │
│  │  • Cost       - 优先选择成本最低的模型              │   │
│  │  • Latency    - 优先选择延迟最低的模型（EMA）       │   │
│  │  • Capability - 根据任务类型选择合适的模型           │   │
│  │  • Fallback   - 使用降级链保证可用性                │   │
│  │  • Weighted   - 按权重随机选择（A/B 测试）          │   │
│  └─────────────────────────────────────────────────┘   │
└────────────────────┬────────────────────────────────────┘
                     │
        ┌────────────┴────────────┐
        │                         │
┌───────▼────────┐       ┌───────▼────────┐
│ Eino OpenAI    │       │ Eino OpenAI    │
│ ChatModel      │       │ ChatModel      │
│ (模型A)        │       │ (模型B)        │
└────────────────┘       └────────────────┘
```

## 技术选型：为什么使用 Eino？

### Eino vs 自研路由

| 维度 | Eino 框架 | 自研路由（旧方案） |
|------|----------|-------------------|
| **维护成本** | 框架维护，跟随社区更新 | 自行维护所有 Provider |
| **抽象层级** | 统一 `ChatModel` 接口 | 自定义 `Provider` 接口 |
| **故障转移** | 内置 `ModelFailoverConfig` | 手动实现降级链 |
| **重试机制** | 内置 `ModelRetryConfig` | 手动实现重试逻辑 |
| **流式处理** | 自动处理 SSE 流 | 手动解析 SSE 数据 |
| **Tool Calling** | 统一 `BindTools` 方法 | 各 Provider 不同实现 |
| **扩展性** | 通过 OpenAI 兼容接口接入任意模型 | 需为每个 Provider 写适配器 |

### Eino 核心优势

1. **统一接口**：所有模型通过 OpenAI 兼容格式接入，无需区分 Claude/OpenAI/GLM/Qwen
2. **内置故障转移**：`ModelFailoverConfig` 提供模型级别的降级机制
3. **流式与非流式统一**：通过 `EnableStreaming` 参数自动切换
4. **工具绑定简化**：`BindTools` 方法统一处理 Function Calling

## 快速开始

### 1. 基础配置

编辑 `configs/config.yaml`：

```yaml
llm:
  models:
    - name: mimo-v2.5
      base_url: https://token-plan-cn.xiaomimimo.com/v1
      api_key: ${MIMO_API_KEY}
      enabled: true
      max_tokens: 300
      temperature: 0.5

    - name: qwen3.5-27b
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
      api_key: ${BAILIAN_API_KEY}
      enabled: true
      max_tokens: 500
      temperature: 0.7

  timeout: 60s
  max_retries: 3
  strategy: capability   # 路由策略
```

### 2. 配置说明

**重要变化**：
- ❌ 不再需要 `type` 字段（已删除）
- ❌ 不再需要 `mode` 字段（已删除）
- ✅ 所有模型统一通过 OpenAI 兼容接口接入
- ✅ 流式/非流式由调用方法自动决定

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | 是 | 模型名称 |
| `base_url` | string | 是 | OpenAI 兼容 API 地址（需以 `/v1` 结尾） |
| `api_key` | string | 是 | API Key，支持 `${ENV_VAR}` 环境变量 |
| `enabled` | bool | 是 | 是否启用 |
| `max_tokens` | int | 否 | 最大生成 token 数 |
| `temperature` | float | 否 | 生成温度 |

### 3. 使用示例

```go
package main

import (
	"context"
	"github.com/watertown/guide/internal/config"
	"github.com/watertown/guide/internal/llm"
)

func main() {
	// 从配置创建 EinoAgentAdapter
	adapter := llm.NewRouterFromConfig(cfg.LLM, logger)

	// 非流式调用
	ctx := context.Background()
	req := &llm.LLMRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "Hello!"},
		},
	}
	resp, err := adapter.Chat(ctx, req)
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Choices[0].Message.Content)

	// 流式调用
	stream, err := adapter.StreamChat(ctx, req)
	if err != nil {
		panic(err)
	}
	for chunk := range stream {
		fmt.Print(chunk.Content)
	}
}
```

## 路由策略

系统支持 6 种路由策略，在配置文件中通过 `strategy` 字段指定：

### 1. Fallback（降级链）— 生产推荐

```yaml
strategy: fallback
```

按配置顺序依次尝试模型，失败后自动切换到下一个。

**Eino 实现**：通过 `ModelFailoverConfig.GetFailoverModel` 返回降级链中的下一个模型。

### 2. Cost（成本优先）

```yaml
strategy: cost
```

选择成本最低的模型（可扩展，需配置价格信息）。

### 3. Latency（延迟优先）

```yaml
strategy: latency
```

根据 EMA 统计选择延迟最低的模型。

**EMA 算法**：新样本权重 30%，历史权重 70%，平滑异常波动。

### 4. Capability（能力优先）

```yaml
strategy: capability
```

根据消息内容自动分类任务类型，选择最适合的模型。

**任务分类优先级**：Code > Reasoning > Chinese > LongText > General。

### 5. Weighted（加权）

```yaml
strategy: weighted
```

按权重随机选择模型，适合 A/B 测试或流量分配。

### 6. Fixed（固定）

```yaml
strategy: fixed
```

始终使用配置中的第一个模型，适合开发调试。

## 流式与非流式调用

### Eino 自动处理

调用方法决定模式：

| 调用方法 | Eino 行为 | HTTP 请求 |
|---------|----------|----------|
| `Chat()` | `EnableStreaming: false` | `stream=false` |
| `StreamChat()` | `EnableStreaming: true` | `stream=true` |

**一个 BaseURL，两种模式**：不需要区分"流式 URL"和"非流式 URL"。

### 流式降级逻辑

当流式调用失败且未返回任何内容时，系统自动降级为非流式调用：

```go
if streamErr != nil && !hasContent {
    // 流式失败 → 降级为非流式
    fallbackCh, fallbackErr := a.fallbackToNonStreaming(ctx, req)
}
```

**降级原因**：某个模型的流式端点可能不稳定，但非流式端点正常。

## Eino ModelFailoverConfig 详解

### 实现方式

项目使用 `github.com/cloudwego/eino/adk` 包中的 `ModelFailoverConfig` 类型语义，但没有直接迁移到 ADK `ChatModelAgent`；而是将 `ReAct Agent` 的 `ToolCallingModel` 替换为一个自定义的 `failoverChatModel` 包装器。

这个包装器：

- 实现 `model.ToolCallingChatModel` 接口
- 持有所有启用的 `eino_openai.ChatModel`
- 在 `Generate` / `Stream` 中先调用主模型，失败后再调用 `GetFailoverModel` 选择下一个模型
- 通过 `ShouldFailover` 判断是否需要降级
- 将实际使用的模型名写入返回消息的 `Extra["model_name"]`，供指标和日志使用

### 配置结构

```go
failoverConfig := &eino_adk.ModelFailoverConfig[*eino_schema.Message]{
    MaxRetries: uint(len(a.models) - 1),  // 最大故障转移尝试次数
    ShouldFailover: func(ctx context.Context, outputMessage *eino_schema.Message, outputErr error) bool {
        // 判断是否需要降级
        if ctx.Err() != nil {
            return false  // 上下文取消，不降级
        }
        return outputErr != nil || (outputMessage != nil && outputMessage.Content == "")
    },
    GetFailoverModel: func(ctx context.Context, failoverCtx *eino_adk.FailoverContext[*eino_schema.Message]) (
        failoverModel eino_model.BaseModel[*eino_schema.Message],
        failoverModelInputMessages []*eino_schema.Message,
        failoverErr error) {
        // 按配置顺序返回下一个模型，优先尝试上次成功的模型
    },
}
```

### 故障转移流程

```
1. 主模型调用失败
2. ShouldFailover 判断是否需要降级
3. GetFailoverModel 返回下一个模型（优先上次成功模型，否则按配置顺序）
4. 继续尝试，直到成功或达到 MaxRetries
5. 所有模型失败 → ReAct agent 返回错误 → agent.Runtime 调用 FallbackAdapter 兜底
```

### 流式故障转移

流式调用同样支持故障转移：

- 先尝试主模型的 `Stream` 初始化
- 如果初始化失败或流读取过程中出现错误，按 `ShouldFailover` 判断
- 满足条件时切换到下一个模型重新流式输出

> 注意：为了检测流式 mid-stream 错误，包装器会先把一份流副本完整消费一遍，确认无误后再返回另一份副本。这会略微改变流式 chunk 到达时间（等效总耗时不变，但首个 chunk 可能稍晚）。


## 工具调用（Function Calling）

### Eino 统一方式

```go
// 定义工具
tools := []llm.LLMTool{
    {
        Type: "function",
        Function: llm.FunctionDef{
            Name:        "get_weather",
            Description: "获取天气信息",
            Parameters: map[string]interface{}{
                "type": "object",
                "properties": map[string]interface{}{
                    "city": map[string]interface{}{
                        "type": "string",
                        "description": "城市名称",
                    },
                },
                "required": []string{"city"},
            },
        },
    },
}

// 通过 LLMRequest 传入
req := &llm.LLMRequest{
    Messages: messages,
    Tools:    tools,
}

// 调用
resp, err := adapter.Chat(ctx, req)

// 检查工具调用
if len(resp.Choices[0].Message.ToolCalls) > 0 {
    toolCall := resp.Choices[0].Message.ToolCalls[0]
    fmt.Printf("调用工具: %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)
}
```

## 模型统计（EMA）

### 统计指标

```go
type modelStats struct {
    totalLatency time.Duration  // 累计延迟
    requestCount int            // 请求次数
    errorCount   int            // 错误次数
}
```

### EMA 计算

```
延迟 EMA：newEMA = 0.3 × currentSample + 0.7 × previousEMA
错误率 EMA：newEMA = 0.3 × (err ? 1.0 : 0.0) + 0.7 × previousEMA
综合评分：Score = Latency + ErrorRate × 10000
```

**设计意图**：
- 错误率放大权重，确保高错误模型被快速降级
- 30% 新样本权重保证快速响应
- 70% 历史权重保证稳定性

## 配置示例

### Claude 模型（通过 OpenAI 兼容接口）

```yaml
- name: claude-3.5-sonnet
  base_url: https://api.anthropic.com/v1  # OpenAI 兼容端点
  api_key: ${ANTHROPIC_API_KEY}
  enabled: true
  max_tokens: 2000
  temperature: 0.7
```

### 小米模型（OpenAI 兼容）

```yaml
- name: mimo-v2.5
  base_url: https://token-plan-cn.xiaomimimo.com/v1
  api_key: ${MIMO_API_KEY}
  enabled: true
  max_tokens: 300
  temperature: 0.5
```

### 阿里云通义千问（OpenAI 兼容）

```yaml
- name: qwen3.5-27b
  base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
  api_key: ${BAILIAN_API_KEY}
  enabled: true
  max_tokens: 500
  temperature: 0.7
```

### 智谱 GLM（OpenAI 兼容）

```yaml
- name: glm-4-flash
  base_url: https://open.bigmodel.cn/api/paas/v4
  api_key: ${GLM_API_KEY}
  enabled: true
  max_tokens: 300
  temperature: 0.7
```

## 设计决策

### 为什么迁移到 Eino？

1. **减少维护成本**：不再需要维护多个 Provider 实现
2. **统一抽象**：所有模型通过同一接口接入
3. **内置可靠性**：Eino 提供故障转移和重试机制
4. **简化配置**：不需要区分 Provider 类型

### 为什么保留流式降级？

Eino 的 `ModelFailoverConfig` 是模型级别的降级，但流式降级是模式级别的：

| 维度 | Eino ModelFailover | 流式降级 |
|------|-------------------|---------|
| **级别** | 模型级别 | 模式级别 |
| **行为** | 模型A失败 → 模型B | 流式失败 → 非流式同模型 |
| **目的** | 模型完全不可用 | 流式端点临时不稳定 |

两者互补，共同保证可用性。

### 为什么保留 EMA 统计？

用于 `Latency` 和 `Weighted` 策略的模型选择决策。Eino 不提供运行时统计，需要自行实现。

## 迁移注意事项

### 从旧系统迁移

| 旧配置 | 新配置 | 说明 |
|-------|-------|------|
| `type: claude` | ❌ 删除 | 不再需要 |
| `mode: auto` | ❌ 删除 | 流式/非流式由调用方法决定 |
| `base_url: .../anthropic` | `base_url: .../v1` | 改为 OpenAI 兼容端点 |

### SDK 依赖变化

| 旧依赖 | 新依赖 |
|-------|-------|
| `anthropic-sdk-go` | ❌ 删除 |
| `go-openai` | ❌ 删除 |
| - | `github.com/cloudwego/eino` |
| - | `github.com/cloudwego/eino-ext/components/model/openai` |

## 常见问题

### Q1: 如何添加新模型？

在配置文件中添加：

```yaml
- name: your-model
  base_url: https://your-api.com/v1  # 必须支持 OpenAI Chat Completions 格式
  api_key: ${YOUR_API_KEY}
  enabled: true
  max_tokens: 300
  temperature: 0.7
```

只要 API 支持 OpenAI Chat Completions 格式即可。

### Q2: Claude 模型如何接入？

使用 OpenAI 兼容端点（如 Anthropic 的 `/v1` 路径），或通过代理服务转换。

### Q3: 流式调用失败会怎样？

1. 如果未返回任何内容 → 自动降级为非流式调用
2. 如果已返回部分内容 → 继续发送，不触发降级
3. 非流式也失败 → Eino ModelFailover 切换到下一个模型

### Q4: 所有模型都失败了怎么办？

FallbackAdapter 返回预设的兜底回复。

### Q5: 如何切换路由策略？

修改配置文件中的 `strategy` 字段：

```yaml
strategy: fallback  # 或 cost/latency/capability/weighted/fixed
```

## 扩展指南

### 添加新的路由策略

在 `multi_model_adapter.go` 中：
1. 定义新策略常量
2. 在 `GetFailoverModel` 中添加分支逻辑
3. 添加相应的配置方法

### 添加模型统计指标

扩展 `modelStats` 结构体，记录更多指标（如成功率、平均延迟等）。

## 技术栈

| 组件 | 技术 |
|------|------|
| LLM 框架 | Eino (CloudWeGo) |
| ChatModel | Eino OpenAI ChatModel |
| Agent | Eino ReAct Agent |
| 故障转移 | 自定义 failoverChatModel + Eino ADK ModelFailoverConfig 语义 |
| 配置管理 | YAML + 环境变量 |


---

*本文档描述 Eino 框架迁移后的多模型路由系统架构。*