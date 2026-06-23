package cost

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/watertown/guide/internal/llm"
	"github.com/watertown/guide/pkg/logging"
)

// LLMSummarizer 基于 LLM 的对话摘要器实现。
type LLMSummarizer struct {
	adapter llm.Adapter
	model   string
	logger  logging.Logger
	timeout time.Duration
}

// NewLLMSummarizer 创建一个 LLM 摘要器。
func NewLLMSummarizer(adapter llm.Adapter, model string, timeout time.Duration, logger logging.Logger) *LLMSummarizer {
	return &LLMSummarizer{
		adapter: adapter,
		model:   model,
		logger:  logger,
		timeout: timeout,
	}
}

// Summarize 对一段对话文本进行摘要。
func (s *LLMSummarizer) Summarize(ctx context.Context, text string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	prompt := fmt.Sprintf(`请将以下对话历史总结成一段简洁的文字，保留关键信息（如用户偏好、重要决策、未完成的任务、名称地点等）。
用中文输出，控制在200字以内。

对话历史：
---
%s
---

摘要：`, text)

	resp, err := s.adapter.Chat(ctx, &llm.LLMRequest{
		Messages: []llm.Message{
			{
				Role:    "system",
				Content: "你是一个专业的对话摘要助手，能够准确提炼对话中的关键信息。请用简洁的中文输出摘要。",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Model:       s.model,
		Temperature: 0.3,
		MaxTokens:   300,
	})
	if err != nil {
		s.logger.Error("LLMSummarizer.Summarize failed", "error", err)
		return "", fmt.Errorf("summarize failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarize returned empty response")
	}

	summary := strings.TrimSpace(resp.Choices[0].Message.Content)
	s.logger.Debug("Summarized", "original_len", len(text), "summary_len", len(summary))
	return summary, nil
}

// IncrementalSummarize 基于已有摘要和新内容，进行增量式更新。
func (s *LLMSummarizer) IncrementalSummarize(ctx context.Context, existingSummary, newContent string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	prompt := fmt.Sprintf(`你需要更新一份对话摘要。以下是现有摘要和新增的对话内容，请将两者合并成一段更新后的摘要，保留所有关键信息。用中文输出，控制在200字以内。

现有摘要：
%s

新增对话内容：
---
%s
---

更新后的摘要：`, existingSummary, newContent)

	resp, err := s.adapter.Chat(ctx, &llm.LLMRequest{
		Messages: []llm.Message{
			{
				Role:    "system",
				Content: "你是一个专业的对话摘要助手，能够准确提炼并合并对话中的关键信息。请用简洁的中文输出摘要。",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Model:       s.model,
		Temperature: 0.3,
		MaxTokens:   300,
	})
	if err != nil {
		s.logger.Error("LLMSummarizer.IncrementalSummarize failed", "error", err)
		return "", fmt.Errorf("incremental summarize failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("incremental summarize returned empty response")
	}

	summary := strings.TrimSpace(resp.Choices[0].Message.Content)
	s.logger.Debug("IncrementalSummary",
		"existing_len", len(existingSummary),
		"new_len", len(newContent),
		"result_len", len(summary),
	)
	return summary, nil
}

// EstimateTokens 估算文本的 Token 数量。
// 中文按每字符约 1.5 tokens，英文按每单词约 1.3 tokens，数字和标点按字符数计算。
func (s *LLMSummarizer) EstimateTokens(text string) int {
	if text == "" {
		return 0
	}

	tokens := 0
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			// 中文字符：约 1.5 tokens/字
			tokens += 2
		} else if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			// 英文字母，在实际单词中处理
			continue
		} else if r == ' ' || r == '\t' || r == '\n' {
			// 空白字符
			continue
		} else {
			// 数字、标点等：约 1 token
			tokens++
		}
	}

	// 英文单词估算：按空格分割后的单词数 * 1.3
	words := strings.Fields(text)
	englishWordCount := 0
	for _, word := range words {
		for _, r := range word {
			// 检查是否主要是英文字母
			if utf8.RuneLen(r) == 1 && ((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				englishWordCount++
				break
			}
		}
	}
	tokens += int(float64(englishWordCount) * 1.3)

	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
