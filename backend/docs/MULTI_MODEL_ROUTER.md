# 多模型路由系统

## 架构概述

多模型路由系统是一个统一的 LLM 服务层，支持多个 AI 提供商（Claude、OpenAI 等），并提供智能路由策略来优化成本、延迟和性能。

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
│                  RouterAdapter                           │
│        (将新系统桥接到旧接口)                              │
└────────────────────┬────────────────────────────────────┘
                     │
                     │ model.ChatRequest/Response
                     │
┌────────────────────▼────────────────────────────────────┐
│                    Router 层                             │
│  ┌─────────────────────────────────────────────────┐   │
│  │  策略引擎                                        │   │
│  │  • Fixed      - 固定使用指定模型                   │   │
│  │  • Cost       - 优先选择成本最低的模型              │   │
│  │  • Latency    - 优先选择延迟最低的模型（EMA）       │   │
│  │  • Capability - 根据任务类型选择合适的模型           │   │
│  │  • Fallback   - 使用降级链保证可用性                │   │
│  │  • Weighted   - 按权重随机选择（A/B 测试）          │   │
│  └─────────────────────────────────────────────────┘   │
│  ┌─────────────────────────────────────────────────┐   │
│  │  ModelStats (EMA)                                │   │
│  │  • 延迟跟踪（指数移动平均）                        │   │
│  │  • 错误率跟踪                                     │   │
│  │  • 综合评分（Score = Latency + ErrorRate * 10000） │   │
│  └─────────────────────────────────────────────────┘   │
└────────────────────┬────────────────────────────────────┘
                     │
        ┌────────────┴────────────┐
        │                         │
┌───────▼────────┐       ┌───────▼────────┐
│ Claude Provider│       │ OpenAI Provider│
│  (anthropic-sdk)│       │  (go-openai)   │
└────────────────┘       └────────────────┘
```

## 快速开始

### 1. 基础用法

```go
package main

import (
	"context"
	"github.com/watertown/guide/internal/llm"
	"github.com/watertown/guide/internal/llm/providers/claude"
	"github.com/watertown/guide/internal/llm/router"
)

func main() {
	// 创建多模型路由器
	multiRouter := llm.NewMultiModelRouter()

	// 添加 Claude provider
	claudeProvider, err := claude.NewProvider(claude.Config{
		APIKey:      "your-anthropic-api-key",
		Model:       "claude-sonnet-4-20250514",
		InputPrice:  0.000003, // $3 per 1M tokens
		OutputPrice: 0.000015, // $15 per 1M tokens
	})
	if err != nil {
		panic(err)
	}
	multiRouter.AddProvider(claudeProvider, true)

	// 设置路由策略
	multiRouter.SetStrategy(router.StrategyFallback)

	// 获取适配器
	adapter := multiRouter.GetAdapter()

	// 使用
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
}
```

### 2. 配置降级链

```go
// 设置降级链：主模型 -> 备选模型 -> 低成本兜底
multiRouter.SetStrategy(router.StrategyFallback)
multiRouter.SetFallbackChain([]string{
	"claude",    // 首选
	"openai",    // 备选
	"claude-haiku", // 低成本兜底
})
```

**设计亮点**：降级链容忍更高错误率，确保可用性优先于成本。

### 3. 基于成本的路由

```go
// 自动选择成本最低的模型
multiRouter.SetStrategy(router.StrategyCost)

// 系统会根据输入 token 估算和价格配置，选择成本最低的模型
// Token 估算：每4字符约1token，中文按字节估算
```

### 4. 基于延迟的路由（EMA）

```go
// 自动选择延迟最低的模型
multiRouter.SetStrategy(router.StrategyLatency)

// 系统使用指数移动平均（EMA）跟踪延迟：
// 指数移动平均：新样本权重 30%，历史权重 70%，平滑异常波动
// 公式：newEMA = 0.3 * currentSample + 0.7 * previousEMA
```

**设计亮点**：EMA 算法平衡了对新数据的响应速度和稳定性。

### 5. 基于任务类型的路由

```go
// 根据消息内容自动分类任务类型，选择最适合的模型
multiRouter.SetStrategy(router.StrategyCapability)

// 任务分类（优先级从高到低）：
// 1. Code      - 代码相关（检测代码关键词和符号）
// 2. Reasoning - 推理任务（检测推理类关键词）
// 3. Chinese   - 中文内容（中文字符占比 > 30%）
// 4. LongText  - 长文本（> 2000 tokens）
// 5. General   - 通用对话

// 配置能力映射
multiRouter.SetCapabilityMap(model.TaskTypeCode, []string{"claude"})
multiRouter.SetCapabilityMap(model.TaskTypeChinese, []string{"openai"})
multiRouter.SetCapabilityMap(model.TaskTypeGeneral, []string{"claude", "openai"})
```

### 6. 加权路由（A/B 测试）

```go
// 按权重随机选择模型，适合 A/B 测试或流量分配
multiRouter.SetStrategy(router.StrategyWeighted)
multiRouter.SetWeight("claude", 0.7)  // 70% 流量
multiRouter.SetWeight("openai", 0.3)  // 30% 流量
```

## 高级功能

### 流式聊天

```go
// 使用 router 的流式 API
router := multiRouter.GetRouter()
stream, err := router.RouteRequestStream(ctx, req)
if err != nil {
	panic(err)
}

for chunk := range stream {
	if chunk.Done {
		fmt.Println("\n[Done]")
		return
	}
	if chunk.Err != nil {
		fmt.Printf("Error: %v\n", chunk.Err)
		return
	}
	fmt.Print(chunk.Content)
}
```

### 工具调用（Function Calling）

```go
// 定义工具
weatherTool := model.Tool{
	Type: "function",
	Function: model.FunctionDef{
		Name:        "get_weather",
		Description: "获取天气信息",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"city": map[string]interface{}{
					"type": "string",
				},
			},
			"required": []string{"city"},
		},
	},
}

req := &model.ChatRequest{
	Model:    "claude-sonnet-4-20250514",
	Messages: messages,
	Tools:    []model.Tool{weatherTool},
}

resp, err := provider.Chat(ctx, req)
if err != nil {
	panic(err)
}

// 检查是否有工具调用
if len(resp.Choices[0].Message.ToolCalls) > 0 {
	toolCall := resp.Choices[0].Message.ToolCalls[0]
	fmt.Printf("调用工具: %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)
}
```

### Token 估算

```go
text := "这是一段中文文本"
tokens := model.EstimateTokens(text)
fmt.Printf("估算 token 数: %d\n", tokens)
// Token 估算：每4字符约1token，中文按字节估算
```

### 模型统计（EMA）

```go
stats := model.NewModelStats()

// 记录延迟
stats.RecordLatency(2 * time.Second)
stats.RecordLatency(3 * time.Second)

// 记录错误
stats.RecordError(false)
stats.RecordError(true)

// 查看统计
fmt.Printf("平均延迟: %.2f ms\n", stats.Latency())
fmt.Printf("错误率: %.2f%%\n", stats.ErrorRate()*100)
fmt.Printf("综合分数: %.2f\n", stats.Score())
// Score = Latency + ErrorRate * 10000
// 错误率以 10 倍权重放大影响，确保高错误模型被快速降级
```

## 配置示例

### 从环境变量初始化

```go
multiRouter := llm.NewMultiModelRouter()

// 从环境变量读取 API keys
configs := map[string]llm.ProviderConfig{
	"claude": {
		Type:        "claude",
		APIKey:      os.Getenv("ANTHROPIC_API_KEY"),
		Model:       "claude-sonnet-4-20250514",
		InputPrice:  0.000003,
		OutputPrice: 0.000015,
		MaxContext:  200000,
		Enabled:     true,
	},
	"openai": {
		Type:        "openai",
		APIKey:      os.Getenv("OPENAI_API_KEY"),
		Model:       "gpt-4o",
		InputPrice:  0.000005,
		OutputPrice: 0.000015,
		MaxContext:  128000,
		Enabled:     true,
	},
}

err := multiRouter.InitializeDefaultProviders(configs)
if err != nil {
	panic(err)
}
```

## 设计亮点总结

### 1. EMA（指数移动平均）算法
```go
// 指数移动平均：新样本权重 30%，历史权重 70%，平滑异常波动
newEMA = 0.3 * currentSample + 0.7 * previousEMA
```
**优势**：平衡响应速度和稳定性，避免单次异常波动影响路由决策。

### 2. 降级链设计
```go
// 降级链容忍更高错误率，确保可用性优先于成本
fallbackChain := []string{"claude", "openai", "claude-haiku"}
```
**优势**：主模型不可用时自动切换到备选，保证服务可用性。

### 3. Token 估算
```go
// Token 估算：每4字符约1token，中文按字节估算
func EstimateTokens(text string) int {
	// 中文字符按字节数 / 3 估算，非中文按字符数 / 4 估算
}
```
**优势**：无需调用 API 即可估算成本，支持成本优化策略。

### 4. 任务分类
```go
// 优先级：Code > Reasoning > Chinese > LongText > General
taskType := ClassifyTask(message)
```
**优势**：根据任务特征自动选择最适合的模型，提升性能。

### 5. 综合评分算法
```go
// Score = Latency + ErrorRate * 10000
// 错误率以 10 倍权重放大影响，确保高错误模型被快速降级
```
**优势**：综合考虑延迟和错误率，避免选择高错误率模型。

## 架构决策

### 为什么使用 Router 模式？
- **解耦**：应用层不关心具体使用哪个模型
- **灵活**：可以动态切换路由策略
- **可观测**：集中收集延迟、错误率等指标

### 为什么使用 EMA 而不是简单平均？
- **响应快**：新数据权重更高，快速适应变化
- **稳定性**：历史数据保留 70% 权重，避免抖动
- **内存高效**：只需保存一个 EMA 值，无需保存所有历史数据

### 为什么降级链优先于成本优化？
- **可用性优先**：用户请求失败比使用更贵的模型更糟糕
- **渐进降级**：从高性能到低成本，平衡质量和成本
- **容错能力**：单一模型故障不影响整体服务

## 扩展指南

### 添加新的 Provider

1. 实现 `model.Provider` 接口：
```go
type Provider interface {
	Name() string
	AvailableModels() []string
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error)
	InputPricePer1K() float64
	OutputPricePer1K() float64
	MaxContextLength() int
}
```

2. 在 `providers/` 目录下创建新的 provider 实现
3. 注册到 router：
```go
multiRouter.AddProvider(newProvider, true)
```

### 添加新的路由策略

1. 在 `router/strategy.go` 中定义新策略
2. 在 `Router.selectProvider()` 中添加分支
3. 添加相应的配置方法

## 性能优化建议

1. **使用 Fallback 策略**：保证可用性，避免单点故障
2. **合理配置降级链**：高性能 -> 标准 -> 低成本
3. **监控 ModelStats**：定期检查各模型的延迟和错误率
4. **使用 Capability 策略**：为不同任务类型选择最优模型
5. **调整 EMA 权重**：根据业务需求调整对新数据的响应速度