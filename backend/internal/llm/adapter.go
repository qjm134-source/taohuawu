package llm

import (
	"context"
)

// LLMRequest LLM 请求
type LLMRequest struct {
	Messages    []Message  `json:"messages"`
	Model       string     `json:"model"`
	Temperature float64    `json:"temperature"`
	MaxTokens   int        `json:"max_tokens"`
	Tools       []LLMTool  `json:"tools,omitempty"` // 可选的工具列表，支持 Function Calling
}

// LLMTool 工具定义，用于 LLM 的 function calling。
type LLMTool struct {
	Type     string           `json:"type"`
	Function LLMFunctionDef    `json:"function"`
}

// LLMFunctionDef 工具的函数定义。
type LLMFunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// LLMResponse LLM 响应
type LLMResponse struct {
	Choices []struct {
		Message      Message      `json:"message"`
		FinishReason string       `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

// ToolCall 表示 LLM 返回的一次工具调用请求。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 描述工具调用的函数名和参数。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// HasToolCalls 判断 LLM 响应是否要求调用工具。
func (r *LLMResponse) HasToolCalls() bool {
	if len(r.Choices) == 0 {
		return false
	}
	return len(r.Choices[0].Message.ToolCalls) > 0
}

// GetToolCalls 获取 LLM 响应中的工具调用列表。
func (r *LLMResponse) GetToolCalls() []ToolCall {
	if !r.HasToolCalls() {
		return nil
	}
	return r.Choices[0].Message.ToolCalls
}

// Message LLM 消息
type Message struct {
	Role       string     `json:"role"` // system, user, assistant
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`  // assistant 消息中的工具调用请求
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool 角色消息对应调用的 ID
}

// Adapter LLM 适配器接口
type Adapter interface {
	Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error)
	IsHealthy() bool
}
