package cost

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/watertown/guide/pkg/logging"
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
	"qwen3.5-27b":       {Input: 0.001, Output: 0.003}, // 阿里云 Qwen 3.5 27B
	"doubao-pro-32k":    {Input: 0.0008, Output: 0.005}, // 字节跳动
	"doubao-pro-128k":   {Input: 0.005, Output: 0.009},
	"glm-4":             {Input: 0.01, Output: 0.01}, // 智谱
	"glm-4-flash":       {Input: 0.0001, Output: 0.0001},
}

// CalculateCost 计算 LLM 调用成本
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	pricing, ok := modelPricing[model]
	if !ok {
		// 默认使用较低的价格估算
		pricing = modelPricing["gpt-3.5-turbo"]
	}

	inputCost := float64(inputTokens) / 1000 * pricing.Input
	outputCost := float64(outputTokens) / 1000 * pricing.Output

	return inputCost + outputCost
}

// Optimizer 成本优化器
type Optimizer struct {
	cache        *LayeredCache
	summary      *Summary
	embeddingAPI EmbeddingAPI
	mu           sync.RWMutex
}

// Summary 摘要
type Summary struct {
	maxMessages    int
	tokenLimit     int
	history        []Message
	currentSummary string
	mu             sync.RWMutex
}

// Message 消息
type Message struct {
	Role      string
	Content   string
	IsSummary bool
}

// Summarizer LLM摘要器接口
type Summarizer interface {
	Summarize(ctx context.Context, text string) (string, error)
	IncrementalSummarize(ctx context.Context, existingSummary, newContent string) (string, error)
	EstimateTokens(text string) int
}

// summarizer 全局摘要器
var summarizer Summarizer

// SetSummarizer 设置摘要器
func SetSummarizer(s Summarizer) {
	summarizer = s
}

// GetSummarizer 获取全局摘要器
func GetSummarizer() Summarizer {
	return summarizer
}

// NewOptimizer 创建优化器
func NewOptimizer(cacheTTL time.Duration, maxMessages, tokenLimit int, embeddingAPI EmbeddingAPI, logger logging.Logger) *Optimizer {
	cacheConfig := CacheConfig{
		Enabled:             true,
		TTL:                 cacheTTL,
		MaxEntries:          1000,
		SimilarityThreshold: 0.85,
	}

	return &Optimizer{
		cache:        NewLayeredCache(cacheConfig, embeddingAPI, logger),
		summary:      NewSummary(maxMessages, tokenLimit),
		embeddingAPI: embeddingAPI,
	}
}

// GetCache 获取缓存（包含语义匹配）
func (o *Optimizer) GetCache(question string) (string, bool) {
	// 先尝试精确匹配
	if answer, ok := o.cache.Get(context.Background(), question, ""); ok {
		return answer, true
	}

	// 如果有 embedding API，尝试语义匹配
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
func NewSummary(maxMessages, tokenLimit int) *Summary {
	return &Summary{
		maxMessages:    maxMessages,
		tokenLimit:     tokenLimit,
		history:        make([]Message, 0, maxMessages),
		currentSummary: "",
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

	// 检查 Token 数量是否超过限制
	if s.tokenLimit > 0 && summarizer != nil {
		totalTokens := s.calculateTotalTokens()
		if totalTokens > s.tokenLimit {
			s.compressWithLLM()
			return
		}
	}

	// 如果超过消息数量阈值，压缩历史
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
	if summarizer == nil {
		return 0
	}
	total := 0
	for _, msg := range s.history {
		total += summarizer.EstimateTokens(msg.Content)
	}
	return total
}

// compress 压缩历史（降级方案）
func (s *Summary) compress() {
	// 如果有摘要器，使用 LLM 压缩
	if summarizer != nil {
		s.compressWithLLM()
		return
	}

	// 简单实现：保留最近的 3 条消息
	if len(s.history) > 3 {
		summary := Message{
			Role:      "system",
			Content:   "之前进行了一些对话，以下是最近的对话内容。",
			IsSummary: true,
		}

		recent := s.history[len(s.history)-3:]
		s.history = append([]Message{summary}, recent...)
	}
}

// compressWithLLM 使用 LLM 进行增量式摘要压缩
func (s *Summary) compressWithLLM() {
	if len(s.history) < 2 {
		return
	}

	// 获取需要压缩的历史消息（保留最近2条作为上下文）
	recentCount := 2
	if len(s.history) < recentCount {
		recentCount = len(s.history)
	}
	recentMessages := s.history[len(s.history)-recentCount:]
	messagesToSummarize := s.history[:len(s.history)-recentCount]

	if len(messagesToSummarize) == 0 {
		return
	}

	// 将消息转换为文本
	textToSummarize := s.messagesToText(messagesToSummarize)

	var newSummary string
	var err error

	// 使用增量式摘要
	if s.currentSummary != "" {
		newSummary, err = summarizer.IncrementalSummarize(context.Background(), s.currentSummary, textToSummarize)
	} else {
		newSummary, err = summarizer.Summarize(context.Background(), textToSummarize)
	}

	if err != nil {
		// 如果 LLM 摘要失败，使用降级方案
		s.compressFallback()
		return
	}

	s.currentSummary = newSummary

	// 构建新的历史：摘要 + 最近消息
	summaryMsg := Message{
		Role:      "system",
		Content:   "[对话摘要] " + newSummary,
		IsSummary: true,
	}

	s.history = append([]Message{summaryMsg}, recentMessages...)
}

// compressFallback 降级压缩方案
func (s *Summary) compressFallback() {
	if len(s.history) > 3 {
		summary := Message{
			Role:      "system",
			Content:   "之前进行了一些对话，以下是最近的对话内容。",
			IsSummary: true,
		}

		recent := s.history[len(s.history)-3:]
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

// embeddingAPI 全局变量用于缓存
var embeddingAPI EmbeddingAPI

// SetEmbeddingAPI 设置 Embedding API
func SetEmbeddingAPI(api EmbeddingAPI) {
	embeddingAPI = api
}
