package model

import (
	"math"
	"sync"
	"time"
)

// ModelStats 跟踪单个 provider / 模型的运行状态。
// 使用指数移动平均（EMA) 平滑延迟和错误率，
// 避免单次异常波动影响路由决策。
type ModelStats struct {
	mu sync.RWMutex

	// avgLatency 是延迟的 EMA 值，单位毫秒。
	avgLatency float64

	// errorRate 是错误率的 EMA 值，范围 [0,1]。
	errorRate float64

	// totalRequests 是总请求数，用于在样本不足时做特殊处理。
	totalRequests int64

	// lastUsed 是最近一次请求的时间。
	lastUsed time.Time
}

// NewModelStats 初始化一个新的 ModelStats。
// 初始延迟给一个中等值，避免冷启动时被路由算法过度惩罚。
func NewModelStats() *ModelStats {
	return &ModelStats{
		avgLatency: 1000.0,
		errorRate:  0.0,
		lastUsed:   time.Now(),
	}
}

// RecordLatency 记录一次请求的延迟。
// 指数移动平均：新样本权重 30%，历史权重 70%，平滑异常波动。
func (s *ModelStats) RecordLatency(latency time.Duration) {
	ms := float64(latency.Milliseconds())

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.totalRequests == 0 {
		s.avgLatency = ms
	} else {
		s.avgLatency = 0.3*ms + 0.7*s.avgLatency
	}
	s.totalRequests++
	s.lastUsed = time.Now()
}

// RecordError 记录一次错误。
// 将错误视为 1，成功视为 0，同样使用 EMA 平滑。
func (s *ModelStats) RecordError(err bool) {
	var value float64
	if err {
		value = 1.0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.totalRequests == 0 {
		s.errorRate = value
	} else {
		s.errorRate = 0.3*value + 0.7*s.errorRate
	}
	s.lastUsed = time.Now()
}

// Latency 返回当前的 EMA 延迟（毫秒）。
func (s *ModelStats) Latency() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.avgLatency
}

// ErrorRate 返回当前的 EMA 错误率。
func (s *ModelStats) ErrorRate() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.errorRate
}

// TotalRequests 返回总请求数。
func (s *ModelStats) TotalRequests() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalRequests
}

// LastUsed 返回最近一次使用时间。
func (s *ModelStats) LastUsed() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastUsed
}

// Score 综合延迟和错误率计算一个分数，越低越好。
// 当样本不足时，使用初始值避免路由决策过于激进。
func (s *ModelStats) Score() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	latencyScore := s.avgLatency
	if s.totalRequests == 0 {
		// 初始延迟设为 1000ms，避免冷启动时被过度打压
		latencyScore = 1000.0
	}

	// 错误率以 10 倍权重放大影响，确保高错误模型被快速降级。
	errorScore := s.errorRate * 10000.0

	return latencyScore + errorScore
}

// EstimateTokens 粗略估算文本对应的 token 数量。
// Token 估算：每4字符约1token，中文按字节估算。
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}

	// 中文字符按字节数 / 3 估算，非中文按字符数 / 4 估算。
	runesCount := 0
	chineseBytes := 0
	for _, r := range text {
		runesCount++
		if r > 127 {
			chineseBytes += 3
		}
	}
	nonChineseChars := runesCount - chineseBytes/3
	if nonChineseChars < 0 {
		nonChineseChars = 0
	}

	tokens := int(math.Ceil(float64(chineseBytes)/3.0 + float64(nonChineseChars)/4.0))
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
