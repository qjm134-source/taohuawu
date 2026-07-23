package cost

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/watertown/guide/pkg/logging"
)

const (
	// defaultPricingModel 是模型未找到定价时的 fallback 模型。
	defaultPricingModel = "gpt-3.5-turbo"

	// defaultSimilarityThreshold 是语义缓存默认相似度阈值。
	defaultSimilarityThreshold = 0.85

	// defaultCacheMaxEntries 是默认最大缓存条目数。
	defaultCacheMaxEntries = 1000

	// fallbackRecentMessages 是降级压缩时保留的最近消息数。
	fallbackRecentMessages = 3

	// llmSummaryRecentMessages 是 LLM 摘要时保留的最近消息数。
	llmSummaryRecentMessages = 2
)

// 模型定价（每 1K tokens，单位：美元）
var modelPricing = map[string]struct {
	Input  float64
	Output float64
}{
	"deepseek-chat":     {Input: 0.0001, Output: 0.0002}, // DeepSeek
	"deepseek-reasoner": {Input: 0.00055, Output: 0.00219},
	"gpt-4o":            {Input: 0.0025, Output: 0.01}, // OpenAI
	"gpt-4o-mini":       {Input: 0.00015, Output: 0.0006},
	"gpt-3.5-turbo":     {Input: 0.0005, Output: 0.0015},
	"claude-3-5-sonnet": {Input: 0.003, Output: 0.015}, // Anthropic
	"claude-3-haiku":    {Input: 0.00025, Output: 0.00125},
	"qwen-turbo":        {Input: 0.0002, Output: 0.0006}, // 阿里云
	"qwen-plus":         {Input: 0.0004, Output: 0.0012},
	"qwen-max":          {Input: 0.002, Output: 0.006},
	"qwen3.5-27b":       {Input: 0.001, Output: 0.003},  // 阿里云 Qwen 3.5 27B
	"doubao-pro-32k":    {Input: 0.0008, Output: 0.005}, // 字节跳动
	"doubao-pro-128k":   {Input: 0.005, Output: 0.009},
	"glm-4":             {Input: 0.01, Output: 0.01}, // 智谱
	"glm-4-flash":       {Input: 0.0001, Output: 0.0001},
}

// CalculateCost 计算 LLM 调用成本（美元）。
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	pricing, ok := modelPricing[model]
	if !ok {
		pricing = modelPricing[defaultPricingModel]
	}

	inputCost := float64(inputTokens) / 1000 * pricing.Input
	outputCost := float64(outputTokens) / 1000 * pricing.Output

	return inputCost + outputCost
}

// Optimizer 成本优化器，负责缓存管理和对话摘要。
type Optimizer struct {
	cache        *LayeredCache
	summary      *Summary
	embeddingAPI EmbeddingAPI
	summarizer   Summarizer
	mu           sync.RWMutex
}

// Summary 摘要，维护一段可压缩的对话历史。
type Summary struct {
	maxMessages    int
	tokenLimit     int
	history        []Message
	currentSummary string
	summarizer     Summarizer
	mu             sync.RWMutex
}

// Message 消息
// Deprecated: 仅用于 Summary 内部表示，外部应使用 agent.Session.Message。
type Message struct {
	Role      string
	Content   string
	IsSummary bool
}

// Summarizer LLM 摘要器接口
type Summarizer interface {
	Summarize(ctx context.Context, text string) (string, error)
	IncrementalSummarize(ctx context.Context, existingSummary, newContent string) (string, error)
	EstimateTokens(text string) int
}

// NewOptimizer 创建优化器
func NewOptimizer(cacheTTL time.Duration, maxMessages, tokenLimit int, embeddingAPI EmbeddingAPI, summarizer Summarizer, logger logging.Logger) *Optimizer {
	cacheConfig := CacheConfig{
		Enabled:             true,
		TTL:                 cacheTTL,
		MaxEntries:          defaultCacheMaxEntries,
		SimilarityThreshold: defaultSimilarityThreshold,
	}

	return &Optimizer{
		cache:        NewLayeredCache(cacheConfig, embeddingAPI, logger),
		summary:      NewSummary(maxMessages, tokenLimit, summarizer),
		embeddingAPI: embeddingAPI,
		summarizer:   summarizer,
	}
}

// Stop 优雅停止 Optimizer 内部资源（如缓存清理 goroutine）。
func (o *Optimizer) Stop() {
	o.cache.Stop()
}

// GetSummarizer 返回优化器持有的摘要器，供外部估算 token 或摘要使用。
func (o *Optimizer) GetSummarizer() Summarizer {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.summarizer
}

// GetCache 获取缓存（包含语义匹配）
func (o *Optimizer) GetCache(question string) (string, bool) {
	if answer, ok := o.cache.Get(context.Background(), question, ""); ok {
		return answer, true
	}

	if o.embeddingAPI != nil {
		if answer, ok, _ := o.cache.GetWithSemantic(context.Background(), question, ""); ok {
			return answer, true
		}
	}

	return "", false
}

// GetCacheWithModel 获取指定模型的缓存
func (o *Optimizer) GetCacheWithModel(question string, model string) (string, bool) {
	return o.cache.Get(context.Background(), question, model)
}

// SetCache 设置缓存
func (o *Optimizer) SetCache(question, answer string) {
	o.cache.Set(context.Background(), question, answer, "", 0)
}

// SetCacheWithModel 设置指定模型的缓存
func (o *Optimizer) SetCacheWithModel(question, answer, model string, tokensSaved int) {
	o.cache.Set(context.Background(), question, answer, model, tokensSaved)
}

// AddHistory 添加历史消息
func (o *Optimizer) AddHistory(role, content string) {
	o.summary.Add(role, content)
}

// GetHistory 获取历史消息
func (o *Optimizer) GetHistory() []Message {
	return o.summary.Get()
}

// CheckSimilarity 检查相似度（语义缓存）
func (o *Optimizer) CheckSimilarity(ctx context.Context, question string, threshold float64) (string, bool) {
	return o.cache.GetSimilar(ctx, question, threshold)
}

// NewSummary 创建摘要
func NewSummary(maxMessages, tokenLimit int, summarizer Summarizer) *Summary {
	return &Summary{
		maxMessages:    maxMessages,
		tokenLimit:     tokenLimit,
		history:        make([]Message, 0, maxMessages),
		currentSummary: "",
		summarizer:     summarizer,
	}
}

// Add 添加消息
func (s *Summary) Add(role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.history = append(s.history, Message{
		Role:    role,
		Content: content,
	})

	if s.tokenLimit > 0 && s.summarizer != nil {
		totalTokens := s.calculateTotalTokens()
		if totalTokens > s.tokenLimit {
			s.compressWithLLM()
			return
		}
	}

	if len(s.history) >= s.maxMessages {
		s.compress()
	}
}

// Get 获取历史
func (s *Summary) Get() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Message, len(s.history))
	copy(result, s.history)
	return result
}

// GetSummary 获取当前摘要
func (s *Summary) GetSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentSummary
}

// calculateTotalTokens 计算总 Token 数
func (s *Summary) calculateTotalTokens() int {
	if s.summarizer == nil {
		return 0
	}
	total := 0
	for _, msg := range s.history {
		total += s.summarizer.EstimateTokens(msg.Content)
	}
	return total
}

// compress 压缩历史（降级方案）
func (s *Summary) compress() {
	if s.summarizer != nil {
		s.compressWithLLM()
		return
	}

	s.compressFallback()
}

// compressWithLLM 使用 LLM 进行增量式摘要压缩
func (s *Summary) compressWithLLM() {
	if len(s.history) < llmSummaryRecentMessages {
		return
	}

	recentCount := llmSummaryRecentMessages
	if len(s.history) < recentCount {
		recentCount = len(s.history)
	}
	recentMessages := s.history[len(s.history)-recentCount:]
	messagesToSummarize := s.history[:len(s.history)-recentCount]

	if len(messagesToSummarize) == 0 {
		return
	}

	textToSummarize := s.messagesToText(messagesToSummarize)

	var newSummary string
	var err error

	if s.currentSummary != "" {
		newSummary, err = s.summarizer.IncrementalSummarize(context.Background(), s.currentSummary, textToSummarize)
	} else {
		newSummary, err = s.summarizer.Summarize(context.Background(), textToSummarize)
	}

	if err != nil {
		s.compressFallback()
		return
	}

	s.currentSummary = newSummary

	summaryMsg := Message{
		Role:      "system",
		Content:   "[对话摘要] " + newSummary,
		IsSummary: true,
	}

	s.history = append([]Message{summaryMsg}, recentMessages...)
}

// compressFallback 降级压缩方案
func (s *Summary) compressFallback() {
	if len(s.history) > fallbackRecentMessages {
		summary := Message{
			Role:      "system",
			Content:   "之前进行了一些对话，以下是最近的对话内容。",
			IsSummary: true,
		}

		recent := s.history[len(s.history)-fallbackRecentMessages:]
		s.history = append([]Message{summary}, recent...)
	}
}

// messagesToText 将消息数组转换为文本
func (s *Summary) messagesToText(messages []Message) string {
	var builder strings.Builder
	for _, msg := range messages {
		if msg.IsSummary {
			builder.WriteString("[摘要] ")
		} else if msg.Role == "user" {
			builder.WriteString("[用户] ")
		} else {
			builder.WriteString("[助手] ")
		}
		builder.WriteString(msg.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}
