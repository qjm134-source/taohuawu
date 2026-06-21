package cost

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// 模型定价（每 1K tokens，单位：美元）
var modelPricing = map[string]struct {
	Input  float64
	Output float64
}{
	"deepseek-chat":            {Input: 0.0001, Output: 0.0002},  // DeepSeek
	"deepseek-reasoner":        {Input: 0.00055, Output: 0.00219},
	"gpt-4o":                   {Input: 0.0025, Output: 0.01},    // OpenAI
	"gpt-4o-mini":              {Input: 0.00015, Output: 0.0006},
	"gpt-3.5-turbo":            {Input: 0.0005, Output: 0.0015},
	"claude-3-5-sonnet":        {Input: 0.003, Output: 0.015},    // Anthropic
	"claude-3-haiku":           {Input: 0.00025, Output: 0.00125},
	"qwen-turbo":               {Input: 0.0002, Output: 0.0006},  // 阿里云
	"qwen-plus":                {Input: 0.0004, Output: 0.0012},
	"qwen-max":                 {Input: 0.002, Output: 0.006},
	"doubao-pro-32k":           {Input: 0.0008, Output: 0.005},   // 字节跳动
	"doubao-pro-128k":          {Input: 0.005, Output: 0.009},
	"glm-4":                    {Input: 0.01, Output: 0.01},      // 智谱
	"glm-4-flash":              {Input: 0.0001, Output: 0.0001},
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
	cache        *Cache
	summary      *Summary
	embeddingAPI EmbeddingAPI
	mu           sync.RWMutex
}

// CacheEntry 缓存条目
type CacheEntry struct {
	Question string
	Answer   string
	CreatedAt time.Time
}

// Cache 缓存
type Cache struct {
	entries map[string]*CacheEntry // key: question hash
	mu      sync.RWMutex
}

// Summary 摘要
type Summary struct {
	maxMessages int
	history     []Message
	mu          sync.RWMutex
}

// Message 消息
type Message struct {
	Role    string
	Content string
	IsSummary bool
}

// EmbeddingAPI Embedding API 接口
type EmbeddingAPI interface {
	GetEmbedding(ctx context.Context, text string) ([]float32, error)
	Similarity(a, b []float32) float64
}

// NewOptimizer 创建优化器
func NewOptimizer(cacheTTL time.Duration, maxMessages int, embeddingAPI EmbeddingAPI) *Optimizer {
	return &Optimizer{
		cache:        NewCache(cacheTTL),
		summary:      NewSummary(maxMessages),
		embeddingAPI: embeddingAPI,
	}
}

// GetCache 获取缓存
func (o *Optimizer) GetCache(question string) (string, bool) {
	return o.cache.Get(question)
}

// SetCache 设置缓存
func (o *Optimizer) SetCache(question, answer string) {
	o.cache.Set(question, answer)
}

// AddHistory 添加历史消息
func (o *Optimizer) AddHistory(role, content string) {
	o.summary.Add(role, content)
}

// GetHistory 获取历史消息
func (o *Optimizer) GetHistory() []Message {
	return o.summary.Get()
}

// CheckSimilarity 检查相似度
func (o *Optimizer) CheckSimilarity(ctx context.Context, question string, threshold float64) (string, bool) {
	if o.embeddingAPI == nil {
		return "", false
	}

	embedding, err := o.embeddingAPI.GetEmbedding(ctx, question)
	if err != nil {
		return "", false
	}

	return o.cache.FindSimilar(embedding, threshold)
}

// NewCache 创建缓存
func NewCache(ttl time.Duration) *Cache {
	c := &Cache{
		entries: make(map[string]*CacheEntry),
	}

	// 启动清理协程
	go c.cleanup(ttl)

	return c
}

// Get 获取缓存
func (c *Cache) Get(question string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := hash(question)
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}

	return entry.Answer, true
}

// Set 设置缓存
func (c *Cache) Set(question, answer string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := hash(question)
	c.entries[key] = &CacheEntry{
		Question: question,
		Answer:   answer,
		CreatedAt: time.Now(),
	}
}

// FindSimilar 查找相似问题
func (c *Cache) FindSimilar(embedding []float32, threshold float64) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, entry := range c.entries {
		entryEmbedding, err := embeddingAPI.GetEmbedding(context.Background(), entry.Question)
		if err != nil {
			continue
		}

		sim := embeddingAPI.Similarity(embedding, entryEmbedding)
		if sim > threshold {
			return entry.Answer, true
		}
	}

	return "", false
}

// cleanup 清理过期缓存
func (c *Cache) cleanup(ttl time.Duration) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for key, entry := range c.entries {
			if now.Sub(entry.CreatedAt) > ttl {
				delete(c.entries, key)
			}
		}
		c.mu.Unlock()
	}
}

// hash 简单哈希
func hash(s string) string {
	return fmt.Sprintf("%x", len(s))
}

// NewSummary 创建摘要
func NewSummary(maxMessages int) *Summary {
	return &Summary{
		maxMessages: maxMessages,
		history:     make([]Message, 0, maxMessages),
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

	// 如果超过阈值，压缩历史
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

// compress 压缩历史
func (s *Summary) compress() {
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

// embeddingAPI 全局变量用于缓存
var embeddingAPI EmbeddingAPI

// SetEmbeddingAPI 设置 Embedding API
func SetEmbeddingAPI(api EmbeddingAPI) {
	embeddingAPI = api
}