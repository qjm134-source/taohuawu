package llm

import (
	"context"
	"io"
	"strings"

	eino_schema "github.com/cloudwego/eino/schema"
)

type FallbackAdapter struct {
	responses map[string]string
}

func NewFallbackAdapter() *FallbackAdapter {
	return &FallbackAdapter{
		responses: map[string]string{
			"default":   "抱歉，我现在无法回答你的问题。请稍后再试，或者查看帮助文档获取更多信息。",
			"welcome":   "欢迎来到江南水乡！我是导游小荷，很高兴为你服务。",
			"operation": "你可以使用键盘 WASD 或方向键移动角色，点击 NPC 进行对话。",
			"task":      "你可以通过点击有感叹号的 NPC 来接取任务。",
			"money":     "完成任务、参与活动都可以赚取金币。",
		},
	}
}

func (a *FallbackAdapter) Chat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (*eino_schema.Message, *ChatUsage, error) {
	userMessage := ""
	for _, msg := range messages {
		if msg.Role == eino_schema.User {
			userMessage = msg.Content
			break
		}
	}

	response := a.matchResponse(userMessage)

	return &eino_schema.Message{
		Role:    eino_schema.Assistant,
		Content: response,
	}, &ChatUsage{Model: "fallback"}, nil
}

func (a *FallbackAdapter) StreamChat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (Stream, error) {
	userMessage := ""
	for _, msg := range messages {
		if msg.Role == eino_schema.User {
			userMessage = msg.Content
			break
		}
	}

	response := a.matchResponse(userMessage)

	return &fallbackStream{
		response:  response,
		pos:       0,
		chunkSize: 10,
	}, nil
}

type fallbackStream struct {
	response  string
	pos       int
	chunkSize int
	closed    bool
}

func (s *fallbackStream) Recv() (*StreamChunk, error) {
	if s.closed {
		return nil, io.EOF
	}

	if s.pos >= len(s.response) {
		s.closed = true
		return &StreamChunk{
			FinishReason: "stop",
			Model:        "fallback",
		}, nil
	}

	end := s.pos + s.chunkSize
	if end > len(s.response) {
		end = len(s.response)
	}

	chunk := &StreamChunk{
		Content: s.response[s.pos:end],
		Model:   "fallback",
	}
	s.pos = end

	return chunk, nil
}

func (s *fallbackStream) Close() error {
	s.closed = true
	return nil
}

func (a *FallbackAdapter) matchResponse(message string) string {
	keywords := map[string]string{
		"欢迎":   "welcome",
		"怎么玩":  "operation",
		"怎么操作": "operation",
		"移动":   "operation",
		"任务":   "task",
		"赚钱":   "money",
		"金币":   "money",
	}

	for kw, key := range keywords {
		if strings.Contains(message, kw) {
			if resp, ok := a.responses[key]; ok {
				return resp
			}
		}
	}

	return a.responses["default"]
}

func (a *FallbackAdapter) IsHealthy() bool {
	return true
}
