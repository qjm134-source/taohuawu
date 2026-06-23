package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/watertown/guide/internal/llm/model"
)

// Provider 是 Claude 服务提供者的实现。
// 它通过 github.com/anthropics/anthropic-sdk-go 与 Anthropic API 交互，
// 支持普通 Chat、流式 Chat 以及 Tool Calling。
type Provider struct {
	client           anthropic.Client
	name             string
	model            string
	inputPrice       float64
	outputPrice      float64
	maxContextLength int
	defaultMaxTokens int64
	logger           *slog.Logger
}

// Config 用于配置 Claude Provider。
type Config struct {
	APIKey           string
	Model            string
	BaseURL          string
	InputPrice       float64
	OutputPrice      float64
	MaxContextLength int
	DefaultMaxTokens int64
}

// NewProvider 创建一个新的 Claude Provider。
func NewProvider(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("claude provider requires api key")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("claude provider requires model name")
	}
	if cfg.DefaultMaxTokens <= 0 {
		cfg.DefaultMaxTokens = 1024
	}

	clientOptions := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		clientOptions = append(clientOptions, option.WithBaseURL(cfg.BaseURL))
	}

	return &Provider{
		client:           anthropic.NewClient(clientOptions...),
		name:             sanitizeProviderName(cfg.Model),
		model:            cfg.Model,
		inputPrice:       cfg.InputPrice,
		outputPrice:      cfg.OutputPrice,
		maxContextLength: cfg.MaxContextLength,
		defaultMaxTokens: cfg.DefaultMaxTokens,
		logger:           slog.Default().With("provider", "claude", "model", cfg.Model),
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
	params, err := p.buildParams(req)
	if err != nil {
		return nil, err
	}

	p.logger.Info("[Claude] Sending chat request",
		"messages_count", len(params.Messages),
		"tools_count", len(params.Tools),
		"has_system", len(params.System) > 0,
		"max_tokens", params.MaxTokens)

	msg, err := p.client.Messages.New(ctx, *params)
	if err != nil {
		p.logger.Error("[Claude] API call failed", "error", err)
		return nil, fmt.Errorf("claude chat failed: %w", err)
	}

	p.logger.Info("[Claude] Raw response received",
		"stop_reason", msg.StopReason,
		"content_blocks", len(msg.Content),
		"input_tokens", msg.Usage.InputTokens,
		"output_tokens", msg.Usage.OutputTokens)

	// 逐块记录 content 类型
	for i, block := range msg.Content {
		p.logger.Info("[Claude] Content block",
			"index", i,
			"type", block.Type,
			"text_len", len(block.Text),
			"tool_use_id", block.ID,
			"tool_use_name", block.Name)
	}

	return p.convertResponse(req.Model, msg), nil
}

// StreamChat 发起一次 SSE 流式对话请求。
func (p *Provider) StreamChat(ctx context.Context, req *model.ChatRequest) (<-chan model.StreamChunk, error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, err
	}

	stream := p.client.Messages.NewStreaming(ctx, *params)

	out := make(chan model.StreamChunk)
	go func() {
		defer close(out)
		index := 0
		for stream.Next() {
			event := stream.Current()
			chunk := p.convertStreamEvent(event)
			if chunk == nil {
				continue
			}
			chunk.Index = index
			index++
			select {
			case <-ctx.Done():
				return
			case out <- *chunk:
			}
		}
		if err := stream.Err(); err != nil {
			select {
			case <-ctx.Done():
				return
			case out <- model.StreamChunk{Done: true, Err: err}:
			}
		}
	}()

	return out, nil
}

// buildParams 将统一的 ChatRequest 转换为 Anthropic 请求参数。
func (p *Provider) buildParams(req *model.ChatRequest) (*anthropic.MessageNewParams, error) {
	messages, systemPrompt, err := p.convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = p.defaultMaxTokens
	}

	params := &anthropic.MessageNewParams{
		Model:     p.model, // 使用 Provider 配置的模型名称，而不是请求中的模型名称
		MaxTokens: maxTokens,
		Messages:  messages,
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	if req.Temperature >= 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	if len(req.Tools) > 0 {
		tools, err := p.convertTools(req.Tools)
		if err != nil {
			return nil, err
		}
		params.Tools = tools
	}

	return params, nil
}

// convertMessages 将统一消息转换为 Anthropic 消息格式。
// Anthropic 的 system prompt 需要单独传入，不能放在 messages 中。
func (p *Provider) convertMessages(msgs []model.Message) ([]anthropic.MessageParam, string, error) {
	var systemPrompt string
	var out []anthropic.MessageParam

	for _, m := range msgs {
		switch m.Role {
		case model.RoleSystem:
			// Anthropic 要求 system 内容单独放在 System 字段。
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += m.Content
		case model.RoleUser:
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case model.RoleAssistant:
			// 跳过既无内容也无工具调用的空 assistant 消息，避免被 API 拒绝。
			if m.Content == "" && len(m.ToolCalls) == 0 {
				continue
			}
			// 仅当 Content 不为空时才创建文本块，避免空字符串文本块被 API 拒绝。
			var blocks []anthropic.ContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var input any
				if tc.Function.Arguments != "" {
					var parsed map[string]any
					if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err == nil {
						input = parsed
					} else {
						input = tc.Function.Arguments
					}
				} else {
					input = map[string]any{}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Function.Name))
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		case model.RoleTool:
			// Tool 角色消息作为 tool_result 块返回给 Claude。
			block := anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false)
			out = append(out, anthropic.NewUserMessage(block))
		default:
			return nil, "", fmt.Errorf("unsupported message role for claude: %s", m.Role)
		}
	}

	return out, systemPrompt, nil
}

// convertTools 将统一工具定义转换为 Anthropic 工具定义。
func (p *Provider) convertTools(tools []model.Tool) ([]anthropic.ToolUnionParam, error) {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		if t.Type != "function" {
			return nil, fmt.Errorf("claude only supports function tools, got %s", t.Type)
		}

		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]any{}
		}

		// 提取 properties（Anthropic API 的 properties 字段必须是对象，不能包含整个 schema）
		properties, _ := schema["properties"].(map[string]any)
		if properties == nil {
			properties = map[string]any{}
		}

		// 提取 required
		required := []string{}
		if req, ok := schema["required"].([]string); ok {
			required = req
		} else if reqIface, ok := schema["required"].([]interface{}); ok {
			for _, v := range reqIface {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}

		toolParam := anthropic.ToolParam{
			Name:        t.Function.Name,
			Description: anthropic.String(t.Function.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: properties,
				Required:   required,
				Type:       "object",
			},
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &toolParam})
	}
	return out, nil
}

// convertResponse 将 Anthropic 响应转换为统一响应。
func (p *Provider) convertResponse(modelName string, msg *anthropic.Message) *model.ChatResponse {
	resp := &model.ChatResponse{
		Model:   modelName,
		Choices: make([]model.Choice, 0),
		Usage: model.Usage{
			PromptTokens:     int(msg.Usage.InputTokens),
			CompletionTokens: int(msg.Usage.OutputTokens),
			TotalTokens:      int(msg.Usage.InputTokens + msg.Usage.OutputTokens),
		},
	}

	choice := model.Choice{Index: 0}
	content := p.extractContent(msg.Content)
	choice.Message = content
	choice.FinishReason = string(msg.StopReason)
	resp.Choices = append(resp.Choices, choice)

	p.logger.Info("[Claude] convertResponse result",
		"content_len", len(content.Content),
		"tool_calls_count", len(content.ToolCalls),
		"finish_reason", choice.FinishReason)

	return resp
}

// extractContent 从 Anthropic 内容块中提取文本和工具调用。
func (p *Provider) extractContent(blocks []anthropic.ContentBlockUnion) model.Message {
	msg := model.Message{Role: model.RoleAssistant}
	var sb strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(block.Text)
		case "thinking":
			// Claude 的思考块，不包含实际文本内容，跳过
			continue
		case "tool_use":
			if block.ID == "" {
				continue
			}
			args, _ := json.Marshal(block.Input)
			msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: model.FunctionCall{
					Name:      block.Name,
					Arguments: string(args),
				},
			})
		}
	}
	msg.Content = sb.String()
	return msg
}

// convertStreamEvent 将流式事件转换为统一 chunk。
func (p *Provider) convertStreamEvent(event anthropic.MessageStreamEventUnion) *model.StreamChunk {
	if event.Type == "content_block_delta" {
		text := event.Delta.Text
		if text != "" {
			return &model.StreamChunk{
				Model:   p.model,
				Content: text,
			}
		}
	}
	return nil
}
