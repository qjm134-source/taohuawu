package cost

import (
	"context"
	"testing"
	"time"
)

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		inputTokens    int
		outputTokens   int
		wantGreaterThan float64
	}{
		{
			name:           "known model deepseek-chat",
			model:          "deepseek-chat",
			inputTokens:    1000,
			outputTokens:   1000,
			wantGreaterThan: 0,
		},
		{
			name:           "unknown model falls back to default",
			model:          "unknown-model",
			inputTokens:    1000,
			outputTokens:   1000,
			wantGreaterThan: 0,
		},
		{
			name:           "zero tokens",
			model:          "gpt-4o",
			inputTokens:    0,
			outputTokens:   0,
			wantGreaterThan: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(tt.model, tt.inputTokens, tt.outputTokens)
			if got <= tt.wantGreaterThan {
				t.Errorf("CalculateCost(%q, %d, %d) = %v, want > %v",
					tt.model, tt.inputTokens, tt.outputTokens, got, tt.wantGreaterThan)
			}
		})
	}
}

func TestCalculateCost_KnownModel(t *testing.T) {
	// gpt-3.5-turbo: input 0.0005, output 0.0015 per 1K tokens
	got := CalculateCost("gpt-3.5-turbo", 2000, 1000)
	want := 0.0005*2 + 0.0015*1
	const epsilon = 1e-9
	if got < want-epsilon || got > want+epsilon {
		t.Errorf("CalculateCost(\"gpt-3.5-turbo\", 2000, 1000) = %v, want %v", got, want)
	}
}

type fakeSummarizer struct {
	estimateTokens int
}

func (f *fakeSummarizer) Summarize(ctx context.Context, text string) (string, error) {
	return "summary: " + text, nil
}

func (f *fakeSummarizer) IncrementalSummarize(ctx context.Context, existingSummary, newContent string) (string, error) {
	return existingSummary + "; " + newContent, nil
}

func (f *fakeSummarizer) EstimateTokens(text string) int {
	return f.estimateTokens
}

func TestSummary_AddAndGet(t *testing.T) {
	s := NewSummary(10, 1000, nil)
	s.Add("user", "hello")
	s.Add("assistant", "hi there")

	msgs := s.Get()
	if len(msgs) != 2 {
		t.Fatalf("len(Get()) = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("first message = %+v, want user/hello", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Errorf("second message = %+v, want assistant/hi there", msgs[1])
	}
}

func TestSummary_CompressByMessageCount(t *testing.T) {
	//  summarizer 为 nil 时，超过 maxMessages 会触发 fallback 压缩
	s := NewSummary(3, 1000, nil)
	s.Add("user", "msg1")
	s.Add("assistant", "msg2")
	s.Add("user", "msg3")
	s.Add("assistant", "msg4")

	msgs := s.Get()
	if len(msgs) != 1+fallbackRecentMessages {
		t.Fatalf("len(Get()) = %d, want %d", len(msgs), 1+fallbackRecentMessages)
	}
	if !msgs[0].IsSummary {
		t.Errorf("first message should be summary, got %+v", msgs[0])
	}
}

func TestSummary_CompressByTokenLimit(t *testing.T) {
	// 每条消息估算 100 tokens，maxMessages 很大，但 tokenLimit 较小
	s := NewSummary(100, 250, &fakeSummarizer{estimateTokens: 100})
	s.Add("user", "msg1")
	s.Add("assistant", "msg2")
	s.Add("user", "msg3")

	msgs := s.Get()
	if len(msgs) == 0 {
		t.Fatal("Get() returned empty messages")
	}
	if !msgs[0].IsSummary {
		t.Errorf("expected compressed summary message, got %+v", msgs[0])
	}
}

func TestOptimizer_GetCacheAndSetCache(t *testing.T) {
	opt := NewOptimizer(time.Minute, 10, 1000, nil, nil, nil)
	defer opt.Stop()

	if _, hit := opt.GetCache("hello"); hit {
		t.Error("GetCache() hit = true before any SetCache")
	}

	opt.SetCache("hello", "world")
	got, hit := opt.GetCache("hello")
	if !hit {
		t.Fatal("GetCache() hit = false after SetCache")
	}
	if got != "world" {
		t.Errorf("GetCache() = %q, want %q", got, "world")
	}
}
