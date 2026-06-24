package openai

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/watertown/guide/internal/llm/model"
)

// Provider 是 OpenAI 服务提供者的实现。
// 它通过 github.com/sashabaranov/go-openai 与 OpenAI API 交互，
// 支持普通 Chat、流式 Chat 以及 Tool Calling。
type Provider struct {
	client           *openai.Client
	name             string
	model            string
	inputPrice       float64
	outputPrice      float64
	maxContextLength int
	defaultMaxTokens int
	logger           *slog.Logger
}

// Config 用于配置 OpenAI Provider。
type Config struct {
	APIKey           string
	Model            string
	BaseURL          string
	InputPrice       float64
	OutputPrice      float64
	MaxContextLength int
	DefaultMaxTokens int
}

// NewProvider 创建一个新的 OpenAI Provider。
func NewProvider(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("openai provider requires api key")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("openai provider requires model name")
	}
	if cfg.DefaultMaxTokens <= 0 {
		cfg.DefaultMaxTokens = 512
	}

	clientConfig := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		clientConfig.BaseURL = cfg.BaseURL
	}

	// 设置 HTTP client 超时，防止无限卡死（context deadline 是主要超时机制，这是兜底保护）
	clientConfig.HTTPClient = &http.Client{
		Timeout: 60 * time.Second,
	}

	// 生成唯一的 provider 名称
	providerName := sanitizeProviderName(cfg.Model)

	return &Provider{
		client:           openai.NewClientWithConfig(clientConfig),
		name:             providerName,
		model:            cfg.Model,
		inputPrice:       cfg.InputPrice,
		outputPrice:      cfg.OutputPrice,
		maxContextLength: cfg.MaxContextLength,
		defaultMaxTokens: cfg.DefaultMaxTokens,
		logger:           slog.Default().With("provider", "openai", "model", cfg.Model),
	}, nil
}

// sanitizeProviderName 将模型名称转换为合法的 provider 标识。
func sanitizeProviderName(name string) string {
	lower := strings.ToLower(name)
	lower = strings.ReplaceAll(lower, ".", "-")
	lower = strings.ReplaceAll(lower, "_", "-")
	return lower
}

// Name 返回 provider 的唯一标识。
func (p *Provider) Name() string { return p.name }

// AvailableModels 返回该 provider 支持的模型。
func (p *Provider) AvailableModels() []string { return []string{p.model} }

// InputPricePer1K 返回输入 token 每 1K 价格。
func (p *Provider) InputPricePer1K() float64 { return p.inputPrice }

// OutputPricePer1K 返回输出 token 每 1K 价格。
func (p *Provider) OutputPricePer1K() float64 { return p.outputPrice }

// MaxContextLength 返回最大上下文长度。
func (p *Provider) MaxContextLength() int { return p.maxContextLength }

// Chat 发起一次非流式对话请求。
func (p *Provider) Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	openaiReq := p.convertRequest(req, false)

	p.logger.Debug("[OpenAI] Sending chat request",
		"messages_count", len(openaiReq.Messages),
		"tools_count", len(openaiReq.Tools),
		"model", openaiReq.Model,
		"max_tokens", openaiReq.MaxTokens)

	resp, err := p.client.CreateChatCompletion(ctx, openaiReq)
	if err != nil {
		p.logger.Error("[OpenAI] API call failed", "error", err)
		return nil, fmt.Errorf("openai chat failed: %w", err)
	}

	p.logger.Debug("[OpenAI] Raw response received",
		"choices_count", len(resp.Choices),
		"model", resp.Model,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens)

	for i, c := range resp.Choices {
		p.logger.Debug("[OpenAI] Choice detail",
			"index", i,
			"finish_reason", c.FinishReason,
			"content_len", len(c.Message.Content),
			"tool_calls_count", len(c.Message.ToolCalls))
	}

	return p.convertResponse(resp), nil
}

// StreamChat 发起一次 SSE 流式对话请求。
func (p *Provider) StreamChat(ctx context.Context, req *model.ChatRequest) (<-chan model.StreamChunk, error) {
	openaiReq := p.convertRequest(req, true)
	stream, err := p.client.CreateChatCompletionStream(ctx, openaiReq)
	if err != nil {
		return nil, fmt.Errorf("openai stream chat failed: %w", err)
	}

	out := make(chan model.StreamChunk)
	go func() {
		defer close(out)
		defer stream.Close()

		index := 0
		for {
			chunk, err := stream.Recv()
			if err != nil {
				if err.Error() != "[DONE]" {
					select {
					case <-ctx.Done():
						return
					case out <- model.StreamChunk{Done: true, Err: err}:
					}
				}
				return
			}

			converted := p.convertStreamChunk(chunk)
			if converted != nil {
				converted.Index = index
				index++
				select {
				case <-ctx.Done():
					return
				case out <- *converted:
				}
			}
		}
	}()

	return out, nil
}

// convertRequest 将统一的 ChatRequest 转换为 OpenAI 请求。
func (p *Provider) convertRequest(req *model.ChatRequest, stream bool) openai.ChatCompletionRequest {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.defaultMaxTokens
	}

	// 使用请求中的 model，如果为空则使用 provider 默认 model
	modelName := req.Model
	if modelName == "" {
		modelName = p.model
	}

	openaiReq := openai.ChatCompletionRequest{
		Model:       modelName,
		Temperature: float32(req.Temperature),
		MaxTokens:   maxTokens,
		Messages:    p.convertMessages(req.Messages),
		Stream:      stream,
	}

	if len(req.Tools) > 0 {
		openaiReq.Tools = p.convertTools(req.Tools)
	}

	return openaiReq
}

// convertMessages 将统一消息转换为 OpenAI 消息格式。
func (p *Provider) convertMessages(msgs []model.Message) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, 0, len(msgs))
	for _, m := range msgs {
		msg := openai.ChatCompletionMessage{
			Role: string(m.Role),
		}

		switch m.Role {
		case model.RoleTool:
			msg.ToolCallID = m.ToolCallID
			msg.Content = m.Content
		case model.RoleAssistant:
			msg.Content = m.Content
			if len(m.ToolCalls) > 0 {
				msg.ToolCalls = p.convertToolCalls(m.ToolCalls)
			}
		default:
			msg.Content = m.Content
		}

		out = append(out, msg)
	}
	return out
}

// convertToolCalls 将统一工具调用转换为 OpenAI 格式。
func (p *Provider) convertToolCalls(calls []model.ToolCall) []openai.ToolCall {
	out := make([]openai.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, openai.ToolCall{
			ID:   c.ID,
			Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		})
	}
	return out
}

// convertTools 将统一工具定义转换为 OpenAI 工具定义。
func (p *Provider) convertTools(tools []model.Tool) []openai.Tool {
	out := make([]openai.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		params := t.Function.Parameters
		if params == nil {
			params = map[string]any{
				"type": "object",
			}
		}
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

// convertResponse 将 OpenAI 响应转换为统一响应。
func (p *Provider) convertResponse(resp openai.ChatCompletionResponse) *model.ChatResponse {
	choices := make([]model.Choice, 0, len(resp.Choices))
	for _, c := range resp.Choices {
		choice := model.Choice{
			Index:        c.Index,
			FinishReason: string(c.FinishReason),
		}

		msg := model.Message{
			Role:    model.Role(c.Message.Role),
			Content: c.Message.Content,
		}

		if len(c.Message.ToolCalls) > 0 {
			msg.ToolCalls = p.convertToolCallsFromResponse(c.Message.ToolCalls)
		}

		choice.Message = msg
		choices = append(choices, choice)
	}

	return &model.ChatResponse{
		Model: resp.Model,
		Usage: model.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
		Choices: choices,
	}
}

// convertToolCallsFromResponse 将 OpenAI 响应中的工具调用转换为统一格式。
func (p *Provider) convertToolCallsFromResponse(calls []openai.ToolCall) []model.ToolCall {
	out := make([]model.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, model.ToolCall{
			ID:   c.ID,
			Type: string(c.Type),
			Function: model.FunctionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		})
	}
	return out
}

// convertStreamChunk 将流式 chunk 转换为统一格式。
func (p *Provider) convertStreamChunk(chunk openai.ChatCompletionStreamResponse) *model.StreamChunk {
	if len(chunk.Choices) == 0 {
		return nil
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// 处理工具调用增量
	if len(delta.ToolCalls) > 0 {
		toolCalls := p.convertToolCallsFromStream(delta.ToolCalls)
		return &model.StreamChunk{
			Model:        p.model,
			ToolCalls:    toolCalls,
			FinishReason: string(choice.FinishReason),
		}
	}

	// 处理文本增量
	if delta.Content != "" {
		return &model.StreamChunk{
			Model:        p.model,
			Content:      delta.Content,
			FinishReason: string(choice.FinishReason),
		}
	}

	// 处理结束
	if string(choice.FinishReason) != "" {
		return &model.StreamChunk{
			Model:        p.model,
			FinishReason: string(choice.FinishReason),
		}
	}

	return nil
}

// convertToolCallsFromStream 将流式工具调用转换为统一格式。
func (p *Provider) convertToolCallsFromStream(calls []openai.ToolCall) []model.ToolCall {
	out := make([]model.ToolCall, 0, len(calls))
	for _, c := range calls {
		tc := model.ToolCall{
			Type: string(c.Type),
			Function: model.FunctionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		}
		if c.ID != "" {
			tc.ID = c.ID
		}
		if c.Index != nil {
			// Index 在流式响应中用于标识工具调用的顺序
			// 在统一格式中，我们使用 ID 来标识
		}
		out = append(out, tc)
	}
	return out
}
