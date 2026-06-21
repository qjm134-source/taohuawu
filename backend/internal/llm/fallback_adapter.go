package llm

import (
	"context"
)

// FallbackAdapter 备用适配器（返回兜底回复）
type FallbackAdapter struct {
	responses map[string]string
}

// NewFallbackAdapter 创建备用适配器
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

// Chat 发送聊天请求
func (a *FallbackAdapter) Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	// 分析用户意图，返回匹配的兜底回复
	userMessage := ""
	if len(req.Messages) > 0 {
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				userMessage = msg.Content
				break
			}
		}
	}

	response := a.matchResponse(userMessage)

	return &LLMResponse{
		Choices: []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		}{
			{
				Message: Message{
					Role:    "assistant",
					Content: response,
				},
				FinishReason: "stop",
			},
		},
		Usage: struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}, nil
}

// matchResponse 匹配响应
func (a *FallbackAdapter) matchResponse(message string) string {
	// 简单关键词匹配
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
		if contains(message, kw) {
			if resp, ok := a.responses[key]; ok {
				return resp
			}
		}
	}

	return a.responses["default"]
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && (s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr))))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// IsHealthy 总是返回 true
func (a *FallbackAdapter) IsHealthy() bool {
	return true
}
