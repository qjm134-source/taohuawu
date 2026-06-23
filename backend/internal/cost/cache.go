package cost

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// CacheType 缓存类型
type CacheType string

const (
	CacheTypeExact       CacheType = "exact"       // 精确匹配缓存
	CacheTypeSemantic    CacheType = "semantic"    // 语义缓存
	CacheTypeToolResult  CacheType = "tool_result" // 工具调用结果缓存
	CacheTypeSummary     CacheType = "summary"     // 对话摘要缓存
)

// CacheConfig 缓存配置
type CacheConfig struct {
	Enabled         bool          // 是否启用缓存
	TTL             time.Duration // 缓存过期时间
	MaxEntries      int           // 最大缓存条目数
	SimilarityThreshold float64    // 语义相似度阈值
}

// CacheEntry 缓存条目
type CacheEntry struct {
	Question     string
	Answer       string
	CreatedAt    time.Time
	Model        string
	TokensSaved  int
	Type         CacheType
}

// Cache 缓存接口
type Cache interface {
	// Get 获取缓存
	Get(ctx context.Context, question string, model string) (string, bool)
	// Set 设置缓存
	Set(ctx context.Context, question string, answer string, model string, tokensSaved int)
	// GetSimilar 获取相似问题的缓存
	GetSimilar(ctx context.Context, question string, threshold float64) (string, bool)
	// GetToolResult 获取工具调用结果缓存
	GetToolResult(ctx context.Context, toolName string, params string) (interface{}, bool)
	// SetToolResult 设置工具调用结果缓存
	SetToolResult(ctx context.Context, toolName string, params string, result interface{})
	// Delete 删除缓存
	Delete(ctx context.Context, question string)
	// Clear 清空所有缓存
	Clear(ctx context.Context)
	// GetStats 获取缓存统计
	GetStats() CacheStats
}

// CacheStats 缓存统计
type CacheStats struct {
	Hits           int64
	Misses         int64
	Entries        int
	MemoryUsageKB  int64
	HitRate        float64
}

// hash 使用 SHA256 生成唯一的缓存键
func hash(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// hashWithModel 生成包含模型信息的缓存键
func hashWithModel(question, model string) string {
	return hash(question + ":" + model)
}