package cost

import (
	"context"
	"sync"
	"time"

	"github.com/watertown/guide/pkg/logging"
)

// LayeredCache 多层缓存实现
type LayeredCache struct {
	config        CacheConfig
	exactCache    map[string]*CacheEntry
	semanticCache map[string]*CacheEntry // key: embedding hash
	toolCache     map[string]interface{}
	embeddingAPI  EmbeddingAPI
	mu            sync.RWMutex
	stats         CacheStats
	ttl           time.Duration
	logger        logging.Logger
}

// NewLayeredCache 创建多层缓存
func NewLayeredCache(config CacheConfig, embeddingAPI EmbeddingAPI, logger logging.Logger) *LayeredCache {
	c := &LayeredCache{
		config:        config,
		exactCache:    make(map[string]*CacheEntry),
		semanticCache: make(map[string]*CacheEntry),
		toolCache:     make(map[string]interface{}),
		embeddingAPI:  embeddingAPI,
		ttl:           config.TTL,
		logger:        logger,
	}

	// 启动定期清理
	go c.cleanup()

	return c
}

// Get 获取缓存（先精确匹配，再语义匹配）
func (c *LayeredCache) Get(ctx context.Context, question string, model string) (string, bool) {
	// 第一层：精确匹配
	if answer, ok := c.getExact(question, model); ok {
		c.stats.Hits++
		return answer, true
	}

	c.stats.Misses++
	return "", false
}

// GetWithSemantic 获取缓存（包含语义匹配）
func (c *LayeredCache) GetWithSemantic(ctx context.Context, question string, model string) (string, bool, CacheType) {
	// 第一层：精确匹配
	if answer, ok := c.getExact(question, model); ok {
		c.stats.Hits++
		return answer, true, CacheTypeExact
	}

	// 第二层：语义匹配（如果启用）
	if c.embeddingAPI != nil {
		if answer, ok := c.getSimilar(ctx, question, c.config.SimilarityThreshold); ok {
			c.stats.Hits++
			return answer, true, CacheTypeSemantic
		}
	}

	c.stats.Misses++
	return "", false, ""
}

// getExact 精确匹配
func (c *LayeredCache) getExact(question string, model string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := hashWithModel(question, model)
	entry, ok := c.exactCache[key]
	if !ok {
		return "", false
	}

	// 检查过期
	if time.Since(entry.CreatedAt) > c.ttl {
		return "", false
	}

	return entry.Answer, true
}

// Set 设置缓存
func (c *LayeredCache) Set(ctx context.Context, question string, answer string, model string, tokensSaved int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 检查容量限制
	if c.config.MaxEntries > 0 && len(c.exactCache) >= c.config.MaxEntries {
		c.evictLRU()
	}

	key := hashWithModel(question, model)
	c.exactCache[key] = &CacheEntry{
		Question:    question,
		Answer:      answer,
		CreatedAt:   time.Now(),
		Model:       model,
		TokensSaved: tokensSaved,
		Type:        CacheTypeExact,
	}

	// 如果有 Embedding API，同时构建语义索引
	if c.embeddingAPI != nil {
		go c.buildSemanticIndex(ctx, question, answer, model, tokensSaved)
	}
}

// buildSemanticIndex 构建语义索引（异步）
func (c *LayeredCache) buildSemanticIndex(ctx context.Context, question, answer, model string, tokensSaved int) {
	embedding, err := c.embeddingAPI.GetEmbedding(ctx, question)
	if err != nil {
		return
	}

	// 使用简单的键（可以使用问题本身或哈希）
	key := hash(question + ":" + model + ":semantic")
	c.mu.Lock()
	c.semanticCache[key] = &CacheEntry{
		Question:    question,
		Answer:      answer,
		CreatedAt:   time.Now(),
		Model:       model,
		TokensSaved: tokensSaved,
		Type:        CacheTypeSemantic,
		Embedding:   embedding, // 存储 embedding
	}
	c.mu.Unlock()
}

// GetSimilar 获取相似问题的缓存
func (c *LayeredCache) GetSimilar(ctx context.Context, question string, threshold float64) (string, bool) {
	if c.embeddingAPI == nil {
		c.logger.Warn("Embedding API is not enabled, cannot get similar questions")
		c.stats.Misses++
		return "", false
	}

	embedding, err := c.embeddingAPI.GetEmbedding(ctx, question)
	if err != nil {
		c.logger.Errorf("Failed to get embedding for question: %v", err)
		c.stats.Misses++
		return "", false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	skippedCount := 0
	for _, entry := range c.semanticCache {
		// 使用缓存的 embedding，避免重复调用 API
		if entry.Embedding == nil || len(entry.Embedding) == 0 {
			skippedCount++
			continue
		}

		sim := c.embeddingAPI.Similarity(embedding, entry.Embedding)
		if sim > threshold {
			return entry.Answer, true
		}
	}

	c.stats.Misses++
	c.logger.Infof("No similar question found, question: %s, skippedCount: %d", question, skippedCount)

	return "", false
}

// getSimilar 内部语义匹配方法
func (c *LayeredCache) getSimilar(ctx context.Context, question string, threshold float64) (string, bool) {
	return c.GetSimilar(ctx, question, threshold)
}

// GetToolResult 获取工具调用结果缓存
func (c *LayeredCache) GetToolResult(ctx context.Context, toolName string, params string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := hash(toolName + ":" + params)
	result, ok := c.toolCache[key]
	return result, ok
}

// SetToolResult 设置工具调用结果缓存
func (c *LayeredCache) SetToolResult(ctx context.Context, toolName string, params string, result interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := hash(toolName + ":" + params)
	c.toolCache[key] = result
}

// Delete 删除缓存
func (c *LayeredCache) Delete(ctx context.Context, question string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, entry := range c.exactCache {
		if entry.Question == question {
			delete(c.exactCache, key)
			break
		}
	}
}

// Clear 清空所有缓存
func (c *LayeredCache) Clear(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.exactCache = make(map[string]*CacheEntry)
	c.semanticCache = make(map[string]*CacheEntry)
	c.toolCache = make(map[string]interface{})
	c.stats = CacheStats{}
}

// evictLRU LRU 淘汰（简单实现：删除最旧的）
func (c *LayeredCache) evictLRU() {
	var oldestKey string
	var oldestTime time.Time

	for key, entry := range c.exactCache {
		if oldestKey == "" || entry.CreatedAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.CreatedAt
		}
	}

	if oldestKey != "" {
		delete(c.exactCache, oldestKey)
	}
}

// cleanup 定期清理过期缓存
func (c *LayeredCache) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()

		// 清理精确缓存
		for key, entry := range c.exactCache {
			if now.Sub(entry.CreatedAt) > c.ttl {
				delete(c.exactCache, key)
			}
		}

		// 清理语义缓存
		for key, entry := range c.semanticCache {
			if now.Sub(entry.CreatedAt) > c.ttl {
				delete(c.semanticCache, key)
			}
		}

		c.mu.Unlock()
	}
}

// GetStats 获取缓存统计
func (c *LayeredCache) GetStats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.stats.Hits + c.stats.Misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(c.stats.Hits) / float64(total) * 100
	}

	return CacheStats{
		Hits:          c.stats.Hits,
		Misses:        c.stats.Misses,
		Entries:       len(c.exactCache) + len(c.semanticCache),
		MemoryUsageKB: int64(len(c.exactCache)+len(c.semanticCache)) * 2, // 估算
		HitRate:       hitRate,
	}
}
