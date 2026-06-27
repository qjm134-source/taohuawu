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

type Adapter interface {
	Chat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (*eino_schema.Message, *ChatUsage, error)
	StreamChat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (Stream, error)
	IsHealthy() bool
}
