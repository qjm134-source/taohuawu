package llm

import (
	"context"
	"errors"
	"io"
	"strings"

	eino_schema "github.com/cloudwego/eino/schema"
)

const (
	fallbackModelName    = "fallback"
	fallbackChunkSize    = 10
	fallbackStreamBuffer = 10
)

// responseKeywords 将用户消息中的关键词映射到回复类别。
// 使用包级变量避免每次 matchResponse 调用时重新分配 map。
var responseKeywords = map[string]string{
	"欢迎":   "welcome",
	"怎么玩":  "operation",
	"怎么操作": "operation",
	"移动":   "operation",
	"任务":   "task",
	"赚钱":   "money",
	"金币":   "money",
}

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
	response := a.matchResponse(extractUserContent(messages))

	return &eino_schema.Message{
		Role:    eino_schema.Assistant,
		Content: response,
	}, &ChatUsage{Model: fallbackModelName}, nil
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
	response := a.matchResponse(extractUserContent(messages))

	streamChan := make(chan *StreamResult, fallbackStreamBuffer)
	done := make(chan struct{})

	go a.streamResponse(ctx, response, streamChan, done)

	return &fallbackEventStream{streamChan: streamChan, done: done}, nil
}

// streamResponse 在 goroutine 中按固定 chunk 大小输出回复，直到输出完毕或收到取消信号。
func (a *FallbackAdapter) streamResponse(ctx context.Context, response string, out chan<- *StreamResult, done <-chan struct{}) {
	defer close(out)

	for pos := 0; pos < len(response); {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		default:
		}

		end := pos + fallbackChunkSize
		if end > len(response) {
			end = len(response)
		}

		if err := sendStreamEvent(ctx, done, out, &StreamResult{
			Event: &StreamEvent{
				Type:    StreamEventTypeChunk,
				Content: response[pos:end],
				Model:   fallbackModelName,
			},
		}); err != nil {
			return
		}

		pos = end
	}

	_ = sendStreamEvent(ctx, done, out, &StreamResult{
		Event: &StreamEvent{
			Type:       StreamEventTypeAction,
			ActionType: "exit",
			Model:      fallbackModelName,
		},
	})
}

func (a *FallbackAdapter) matchResponse(message string) string {
	for kw, key := range responseKeywords {
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

// extractUserContent 从消息列表中提取第一条用户消息内容。
func extractUserContent(messages []*eino_schema.Message) string {
	for _, msg := range messages {
		if msg.Role == eino_schema.User {
			return msg.Content
		}
	}
	return ""
}

// sendStreamEvent 尝试将事件发送到输出 channel，若 context 取消或收到 done 信号则返回错误。
func sendStreamEvent(ctx context.Context, done <-chan struct{}, out chan<- *StreamResult, result *StreamResult) error {
	select {
	case out <- result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.New("stream closed")
	}
}
