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

type fallbackEventStream struct {
	streamChan <-chan *StreamResult
	done       chan struct{}
}

func (s *fallbackEventStream) Recv() (*StreamEvent, error) {
	result, ok := <-s.streamChan
	if !ok {
		return nil, io.EOF
	}
	if result.Err != nil {
		return nil, result.Err
	}
	return result.Event, nil
}

func (s *fallbackEventStream) Close() {
	close(s.done)
}

func (a *FallbackAdapter) StreamChat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (EventStream, error) {
	userMessage := ""
	for _, msg := range messages {
		if msg.Role == eino_schema.User {
			userMessage = msg.Content
			break
		}
	}

	response := a.matchResponse(userMessage)

	streamChan := make(chan *StreamResult, 10)
	done := make(chan struct{})

	go func() {
		defer close(streamChan)

		pos := 0
		chunkSize := 10

		for pos < len(response) {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			default:
			}

			end := pos + chunkSize
			if end > len(response) {
				end = len(response)
			}

			select {
			case streamChan <- &StreamResult{
				Event: &StreamEvent{
					Type:    StreamEventTypeChunk,
					Content: response[pos:end],
					Model:   "fallback",
				},
			}:
			case <-ctx.Done():
				return
			case <-done:
				return
			}

			pos = end
		}

		select {
		case streamChan <- &StreamResult{
			Event: &StreamEvent{
				Type:       StreamEventTypeAction,
				ActionType: "exit",
				Model:      "fallback",
			},
		}:
		case <-ctx.Done():
		case <-done:
		}
	}()

	return &fallbackEventStream{streamChan: streamChan, done: done}, nil
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
