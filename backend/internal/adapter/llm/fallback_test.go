package llm

import (
	"context"
	"io"
	"testing"

	eino_schema "github.com/cloudwego/eino/schema"
)

func TestFallbackAdapter_Chat_DefaultResponse(t *testing.T) {
	a := NewFallbackAdapter()

	msgs := []*eino_schema.Message{
		{Role: eino_schema.User, Content: "你好，这是一条普通消息"},
	}

	resp, usage, err := a.Chat(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Chat() error = %v, want nil", err)
	}
	if resp == nil {
		t.Fatal("Chat() resp = nil, want non-nil")
	}
	if resp.Role != eino_schema.Assistant {
		t.Errorf("resp.Role = %v, want %v", resp.Role, eino_schema.Assistant)
	}
	if resp.Content == "" {
		t.Error("resp.Content is empty, want non-empty default response")
	}
	if usage == nil {
		t.Fatal("Chat() usage = nil, want non-nil")
	}
	if usage.Model != fallbackModelName {
		t.Errorf("usage.Model = %q, want %q", usage.Model, fallbackModelName)
	}
}

func TestFallbackAdapter_MatchResponse(t *testing.T) {
	a := NewFallbackAdapter()

	tests := []struct {
		name    string
		message string
		wantKey string // response key, not exact content
	}{
		{
			name:    "welcome keyword",
			message: "欢迎来到这个游戏",
			wantKey: "welcome",
		},
		{
			name:    "operation keyword 怎么玩",
			message: "请问这个游戏怎么玩？",
			wantKey: "operation",
		},
		{
			name:    "operation keyword 移动",
			message: "角色怎么移动？",
			wantKey: "operation",
		},
		{
			name:    "task keyword",
			message: "任务在哪里接？",
			wantKey: "task",
		},
		{
			name:    "money keyword 金币",
			message: "金币怎么赚？",
			wantKey: "money",
		},
		{
			name:    "no match returns default",
			message: "完全不相关的内容 abcdef",
			wantKey: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.matchResponse(tt.message)
			want := a.responses[tt.wantKey]
			if got != want {
				t.Errorf("matchResponse(%q) = %q, want %q (key %q)", tt.message, got, want, tt.wantKey)
			}
		})
	}
}

func TestExtractUserContent(t *testing.T) {
	tests := []struct {
		name     string
		messages []*eino_schema.Message
		want     string
	}{
		{
			name:     "single user message",
			messages: []*eino_schema.Message{{Role: eino_schema.User, Content: "hello"}},
			want:     "hello",
		},
		{
			name: "picks first user message",
			messages: []*eino_schema.Message{
				{Role: eino_schema.System, Content: "sys"},
				{Role: eino_schema.User, Content: "first user"},
				{Role: eino_schema.User, Content: "second user"},
			},
			want: "first user",
		},
		{
			name:     "no user message returns empty",
			messages: []*eino_schema.Message{{Role: eino_schema.System, Content: "sys"}},
			want:     "",
		},
		{
			name:     "nil messages returns empty",
			messages: nil,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractUserContent(tt.messages)
			if got != tt.want {
				t.Errorf("extractUserContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFallbackAdapter_StreamChat(t *testing.T) {
	a := NewFallbackAdapter()

	msgs := []*eino_schema.Message{
		{Role: eino_schema.User, Content: "欢迎"},
	}

	stream, err := a.StreamChat(context.Background(), msgs)
	if err != nil {
		t.Fatalf("StreamChat() error = %v, want nil", err)
	}
	if stream == nil {
		t.Fatal("StreamChat() stream = nil, want non-nil")
	}
	defer stream.Close()

	// 收集所有事件
	var chunks []string
	var hasExit bool
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv() error = %v, want nil or io.EOF", err)
		}
		if event == nil {
			t.Fatal("stream.Recv() returned nil event without error")
		}

		switch event.Type {
		case StreamEventTypeChunk:
			chunks = append(chunks, event.Content)
			if event.Model != fallbackModelName {
				t.Errorf("chunk model = %q, want %q", event.Model, fallbackModelName)
			}
		case StreamEventTypeAction:
			if event.ActionType == "exit" {
				hasExit = true
			}
		}
	}

	if len(chunks) == 0 {
		t.Error("no chunk events received, want at least one")
	}
	if !hasExit {
		t.Error("no exit action event received, want one")
	}

	// 拼接 chunk 内容应该等于 welcome 响应
	var fullContent string
	for _, c := range chunks {
		fullContent += c
	}
	want := a.responses["welcome"]
	if fullContent != want {
		t.Errorf("streamed content = %q, want %q", fullContent, want)
	}
}

func TestFallbackAdapter_StreamChat_ContextCancel(t *testing.T) {
	a := NewFallbackAdapter()

	msgs := []*eino_schema.Message{
		{Role: eino_schema.User, Content: "欢迎"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := a.StreamChat(ctx, msgs)
	if err != nil {
		t.Fatalf("StreamChat() error = %v, want nil", err)
	}
	// 立即取消，确保 goroutine 能退出
	cancel()
	stream.Close()
}

func TestFallbackAdapter_IsHealthy(t *testing.T) {
	a := NewFallbackAdapter()
	if !a.IsHealthy() {
		t.Error("IsHealthy() = false, want true")
	}
}
