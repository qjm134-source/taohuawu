package model

// Choice 表示 LLM 返回的一个候选结果。
type Choice struct {
	Index int `json:"index"`

	// Message 包含 assistant 的回复，可能携带 ToolCalls。
	Message Message `json:"message"`

	// FinishReason 表示生成结束的原因，例如 "stop"、"tool_calls"、"length"。
	FinishReason string `json:"finish_reason"`
}

// Usage 记录一次请求的 token 消耗。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse 是统一的 LLM 非流式响应结构。
type ChatResponse struct {
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// StreamChunk 表示 SSE 流式输出中的单个数据块。
type StreamChunk struct {
	// Model 是生成该响应的模型名称。
	Model string `json:"model"`

	// Index 是该 chunk 在流中的序号，从 0 开始。
	Index int

	// Content 是本次增量文本。
	Content string `json:"content"`

	// ToolCalls 是本次增量的工具调用片段，仅在工具调用流中出现。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// FinishReason 表示流是否结束。
	FinishReason string `json:"finish_reason,omitempty"`

	// Done 表示整个 SSE 流是否已经结束。
	Done bool `json:"done"`

	// Err 携带流处理过程中出现的错误。
	Err error `json:"-"`
}

// IsToolCall 判断该 chunk 是否包含工具调用信息。
func (c *StreamChunk) IsToolCall() bool {
	return len(c.ToolCalls) > 0
}
