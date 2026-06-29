package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// LLM 调用指标
	LLMRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_requests_total",
			Help: "Total number of LLM requests",
		},
		[]string{"model", "status"},
	)

	LLMRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llm_request_duration_seconds",
			Help:    "LLM request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"model"},
	)

	LLMRequestTokens = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_request_tokens_total",
			Help: "Total number of LLM request tokens",
		},
		[]string{"model"},
	)

	LLMCompletionTokens = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_completion_tokens_total",
			Help: "Total number of LLM completion tokens",
		},
		[]string{"model"},
	)

	// WebSocket 连接指标
	WebSocketConnections = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "websocket_connections",
			Help: "Current number of WebSocket connections",
		},
		[]string{"tenant_id"},
	)

	WebSocketMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "websocket_messages_total",
			Help: "Total number of WebSocket messages",
		},
		[]string{"type", "direction"},
	)

	// 成本指标
	CostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cost_total",
			Help: "Total LLM cost",
		},
		[]string{"model"},
	)

	// 缓存指标
	CacheHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_hits_total",
			Help: "Total number of cache hits",
		},
		[]string{"cache_type"},
	)

	CacheMissesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_misses_total",
			Help: "Total number of cache misses",
		},
		[]string{"cache_type"},
	)

	CacheHitRatio = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "cache_hit_ratio",
			Help: "Cache hit ratio",
		},
	)

	// Agent 指标
	AgentRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "agent_requests_total",
			Help: "Total number of agent requests",
		},
		[]string{"action", "status"},
	)

	AgentRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "agent_request_duration_seconds",
			Help:    "Agent request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"action"},
	)
)
