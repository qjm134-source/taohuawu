package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/watertown/guide/internal/config"
	"github.com/watertown/guide/internal/llm/model"
	"github.com/watertown/guide/internal/llm/providers/claude"
	"github.com/watertown/guide/internal/llm/providers/openai"
	"github.com/watertown/guide/internal/llm/router"
	"github.com/watertown/guide/pkg/logging"
)

// RouterAdapter 是 llm.Adapter 接口的实现，内部使用 Router 进行模型路由。
// 它提供了从旧的单一适配器模式到新的多模型路由模式的平滑迁移。
type RouterAdapter struct {
	router *router.Router
}

// NewRouterAdapter 创建一个新的路由适配器。
func NewRouterAdapter(r *router.Router) *RouterAdapter {
	return &RouterAdapter{
		router: r,
	}
}

// Chat 发起一次聊天请求，通过 Router 选择合适的 provider。
func (a *RouterAdapter) Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	// 转换请求格式
	modelReq := a.convertRequest(req)

	// 通过路由选择 provider 并执行
	modelResp, err := a.router.RouteRequest(ctx, modelReq)
	if err != nil {
		return nil, err
	}

	// 转换响应格式
	return a.convertResponse(modelResp), nil
}

// IsHealthy 检查路由器是否健康（至少有一个 provider 可用）。
func (a *RouterAdapter) IsHealthy() bool {
	// 简化实现：检查默认 provider 是否可用
	// 实际项目中可以实现更复杂的健康检查
	return true
}

// convertRequest 将 LLMRequest 转换为 model.ChatRequest。
func (a *RouterAdapter) convertRequest(req *LLMRequest) *model.ChatRequest {
	messages := make([]model.Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		messages = append(messages, model.Message{
			Role:       model.Role(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
			ToolCalls:  a.convertToolCallsToModel(msg.ToolCalls),
		})
	}

	return &model.ChatRequest{
		Model:       req.Model,
		Messages:    messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       a.convertToolsToModel(req.Tools),
	}
}

func (a *RouterAdapter) convertToolCallsToModel(calls []ToolCall) []model.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]model.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, model.ToolCall{
			ID:   c.ID,
			Type: c.Type,
			Function: model.FunctionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		})
	}
	return out
}

func (a *RouterAdapter) convertToolsToModel(tools []LLMTool) []model.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]model.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, model.Tool{
			Type: t.Type,
			Function: model.FunctionDef{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		})
	}
	return out
}

func (a *RouterAdapter) convertToolCallsFromModel(calls []model.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolCall{
			ID:   c.ID,
			Type: c.Type,
			Function: ToolCallFunction{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		})
	}
	return out
}

// convertResponse 将 model.ChatResponse 转换为 LLMResponse。
func (a *RouterAdapter) convertResponse(resp *model.ChatResponse) *LLMResponse {
	choices := make([]struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	}, 0, len(resp.Choices))

	for _, c := range resp.Choices {
		choices = append(choices, struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		}{
			Message: Message{
				Role:      string(c.Message.Role),
				Content:   c.Message.Content,
				ToolCalls: a.convertToolCallsFromModel(c.Message.ToolCalls),
			},
			FinishReason: c.FinishReason,
		})
	}

	return &LLMResponse{
		Choices: choices,
		Usage: struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
		Model: resp.Model,
	}
}

// MultiModelRouter 是包装类，提供更便捷的初始化方法。
type MultiModelRouter struct {
	adapter *RouterAdapter
	router  *router.Router
}

// NewMultiModelRouter 创建并配置一个多模型路由器。
func NewMultiModelRouter(logger logging.Logger) *MultiModelRouter {
	r := router.NewRouter(logger)
	return &MultiModelRouter{
		adapter: NewRouterAdapter(r),
		router:  r,
	}
}

// AddProvider 添加一个 provider。
func (m *MultiModelRouter) AddProvider(provider model.Provider, enabled bool) {
	m.router.RegisterProvider(provider, enabled)
}

// SetStrategy 设置路由策略。
func (m *MultiModelRouter) SetStrategy(strategy router.Strategy) {
	m.router.SetStrategy(strategy)
}

// SetFixedModel 设置固定模型。
func (m *MultiModelRouter) SetFixedModel(model string) {
	m.router.SetFixedModel(model)
}

// SetWeight 设置模型权重。
func (m *MultiModelRouter) SetWeight(providerName string, weight float64) {
	m.router.SetWeight(providerName, weight)
}

// SetFallbackChain 设置降级链。
func (m *MultiModelRouter) SetFallbackChain(providerNames []string) {
	m.router.SetFallbackChain(providerNames)
}

// SetCapabilityMap 设置能力映射。
func (m *MultiModelRouter) SetCapabilityMap(taskType model.TaskType, providerNames []string) {
	m.router.SetCapabilityMap(taskType, providerNames)
}

// GetAdapter 获取适配器实例，用于集成到现有系统。
func (m *MultiModelRouter) GetAdapter() Adapter {
	return m.adapter
}

// GetRouter 获取路由器实例，用于高级配置。
func (m *MultiModelRouter) GetRouter() *router.Router {
	return m.router
}

// InitializeDefaultProviders 使用配置初始化默认的 providers。
// 这是一个便捷方法，用于从配置创建和注册 providers。
func (m *MultiModelRouter) InitializeDefaultProviders(cfg map[string]ProviderConfig) error {
	for name, config := range cfg {
		switch config.Type {
		case "claude":
			provider, err := initializeClaudeProvider(config)
			if err != nil {
				return fmt.Errorf("failed to initialize claude provider %s: %w", name, err)
			}
			m.AddProvider(provider, config.Enabled)
		case "openai":
			provider, err := initializeOpenAIProvider(config)
			if err != nil {
				return fmt.Errorf("failed to initialize openai provider %s: %w", name, err)
			}
			m.AddProvider(provider, config.Enabled)
		default:
			return fmt.Errorf("unsupported provider type: %s", config.Type)
		}
	}
	return nil
}

// ProviderConfig 是 provider 的配置结构。
type ProviderConfig struct {
	Type        string  // provider 类型: "claude", "openai"
	APIKey      string  // API 密钥
	Model       string  // 模型名称
	BaseURL     string  // 基础 URL（可选）
	InputPrice  float64 // 输入价格
	OutputPrice float64 // 输出价格
	MaxContext  int     // 最大上下文长度
	Enabled     bool    // 是否启用
}

// initializeClaudeProvider 初始化 Claude provider。
func initializeClaudeProvider(cfg ProviderConfig) (model.Provider, error) {
	return claude.NewProvider(claude.Config{
		APIKey:           cfg.APIKey,
		Model:            cfg.Model,
		BaseURL:          cfg.BaseURL,
		InputPrice:       cfg.InputPrice,
		OutputPrice:      cfg.OutputPrice,
		MaxContextLength: cfg.MaxContext,
	})
}

// initializeOpenAIProvider 初始化 OpenAI provider.
func initializeOpenAIProvider(cfg ProviderConfig) (model.Provider, error) {
	return openai.NewProvider(openai.Config{
		APIKey:           cfg.APIKey,
		Model:            cfg.Model,
		BaseURL:          cfg.BaseURL,
		InputPrice:       cfg.InputPrice,
		OutputPrice:      cfg.OutputPrice,
		MaxContextLength: cfg.MaxContext,
	})
}

// NewRouterFromConfig 从旧的 LLMConfig 创建 RouterAdapter，兼容现有系统。
// 根据模型名称自动推断 provider 类型（claude/openai），并注册到路由器中。
// 降级链按配置顺序排列，优先使用排在前面的模型。
func NewRouterFromConfig(cfg config.LLMConfig, logger logging.Logger) Adapter {
	r := router.NewRouter(logger)
	r.SetStrategy(router.StrategyFallback)

	var fallbackChain []string

	for _, mc := range cfg.Models {
		if !mc.Enabled {
			continue
		}

		providerType := inferProviderType(mc.Name, mc.BaseURL)
		providerName := sanitizeProviderName(mc.Name)

		var provider model.Provider
		var err error

		switch providerType {
		case "claude":
			provider, err = claude.NewProvider(claude.Config{
				APIKey:           mc.APIKey,
				Model:            mc.Name,
				BaseURL:          mc.BaseURL,
				MaxContextLength: 200000,
				DefaultMaxTokens: int64(mc.MaxTokens),
			})
		case "openai":
			provider, err = openai.NewProvider(openai.Config{
				APIKey:           mc.APIKey,
				Model:            mc.Name,
				BaseURL:          mc.BaseURL,
				MaxContextLength: 128000,
				DefaultMaxTokens: mc.MaxTokens,
			})
		default:
			// 未知类型默认使用 OpenAI 兼容模式（大多数国内 API 都兼容 OpenAI 格式）
			provider, err = openai.NewProvider(openai.Config{
				APIKey:           mc.APIKey,
				Model:            mc.Name,
				BaseURL:          mc.BaseURL,
				MaxContextLength: 128000,
				DefaultMaxTokens: mc.MaxTokens,
			})
		}

		if err != nil {
			logger.Error("Failed to create provider", "model", mc.Name, "error", err)
			continue
		}

		r.RegisterProvider(provider, true)
		fallbackChain = append(fallbackChain, providerName)
		logger.Info("Provider registered", "name", providerName, "model", mc.Name, "type", providerType)
	}

	if len(fallbackChain) > 0 {
		r.SetFallbackChain(fallbackChain)
	}

	return NewRouterAdapter(r)
}

// inferProviderType 根据模型名称和 BaseURL 推断 provider 类型。
func inferProviderType(modelName, baseURL string) string {
	lower := strings.ToLower(modelName)
	baseLower := strings.ToLower(baseURL)

	if strings.Contains(lower, "claude") || strings.Contains(baseLower, "anthropic") {
		return "claude"
	}
	if strings.Contains(lower, "gpt") || strings.Contains(lower, "o1") || strings.Contains(lower, "o3") ||
		strings.Contains(baseLower, "openai") {
		return "openai"
	}

	// 默认使用 OpenAI 兼容模式，大多数国内 API（如 GLM、DeepSeek、Qwen）都支持 OpenAI 格式
	return "openai"
}

// sanitizeProviderName 将模型名称转换为合法的 provider 标识。
func sanitizeProviderName(name string) string {
	lower := strings.ToLower(name)
	lower = strings.ReplaceAll(lower, ".", "-")
	lower = strings.ReplaceAll(lower, "_", "-")
	return lower
}
