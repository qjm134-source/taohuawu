package llm

import (
	"context"

	eino_schema "github.com/cloudwego/eino/schema"
)

type ChatOptions struct {
	Temperature float32
	MaxTokens   int
}

type ChatOption func(*ChatOptions)

func WithTemperature(t float32) ChatOption {
	return func(o *ChatOptions) { o.Temperature = t }
}

func WithMaxTokens(m int) ChatOption {
	return func(o *ChatOptions) { o.MaxTokens = m }
}

type ChatUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Model            string
}

type StreamChunk struct {
	Content      string
	FinishReason string
	Model        string
	Usage        ChatUsage
}

type Stream interface {
	Recv() (*StreamChunk, error)
	Close() error
}

type StreamEventType string

const (
	StreamEventTypeChunk      StreamEventType = "stream_chunk"
	StreamEventTypeToolCalls  StreamEventType = "tool_calls"
	StreamEventTypeToolResult StreamEventType = "tool_result"
	StreamEventTypeAction     StreamEventType = "action"
)

type ToolCall struct {
	ID       string                 `json:"id"`
	ToolName string                 `json:"tool_name"`
	Params   map[string]interface{} `json:"params"`
}

type StreamEvent struct {
	Type         StreamEventType `json:"type"`
	Content      string          `json:"content,omitempty"`
	ToolCalls    []ToolCall      `json:"tool_calls,omitempty"`
	ToolResult   string          `json:"tool_result,omitempty"`
	ActionType   string          `json:"action_type,omitempty"`
	Model        string          `json:"model,omitempty"`
	Usage        *ChatUsage      `json:"usage,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
}

type EventStream interface {
	Recv() (*StreamEvent, error)
	Close() error
}

type Adapter interface {
	Chat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (*eino_schema.Message, *ChatUsage, error)
	StreamChat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (EventStream, error)
	IsHealthy() bool
}
