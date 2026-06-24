package observability

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	// HTTPRequestsTotal HTTP 请求计数器
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	// HTTPRequestDuration HTTP 请求耗时
	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	// HTTPInFlightRequests 当前正在处理的请求数
	HTTPInFlightRequests = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Current number of HTTP requests being handled",
		},
	)
)

func init() {
	prometheus.MustRegister(HTTPRequestsTotal)
	prometheus.MustRegister(HTTPRequestDuration)
	prometheus.MustRegister(HTTPInFlightRequests)
}

// PrometheusMiddleware 返回 Prometheus HTTP 指标的 Gin 中间件。
func PrometheusMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		HTTPInFlightRequests.Inc()

		c.Next()

		HTTPInFlightRequests.Dec()
		duration := time.Since(start).Seconds()
		status := fmt.Sprintf("%d", c.Writer.Status())
		method := c.Request.Method
		path := c.FullPath() // 使用路由模板路径，而非实际路径（避免高基数）

		if path == "" {
			path = c.Request.URL.Path
		}

		HTTPRequestsTotal.WithLabelValues(method, path, status).Inc()
		HTTPRequestDuration.WithLabelValues(method, path).Observe(duration)
	}
}

// TracingMiddleware 返回 OpenTelemetry 分布式追踪的 Gin 中间件。
func TracingMiddleware(serviceName string) gin.HandlerFunc {
	propagator := propagation.TraceContext{}

	return func(c *gin.Context) {
		// 从请求头中提取 Trace Context
		ctx := propagator.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		// 创建根 Span
		spanName := c.Request.Method + " " + c.FullPath()
		if c.FullPath() == "" {
			spanName = c.Request.Method + " " + c.Request.URL.Path
		}

		opts := []trace.SpanStartOption{
			trace.WithAttributes(
				semconv.HTTPMethodKey.String(c.Request.Method),
				semconv.HTTPURLKey.String(c.Request.URL.String()),
				semconv.HTTPSchemeKey.String(c.Request.URL.Scheme),
				semconv.NetHostNameKey.String(c.Request.Host),
				attribute.String("http.target", c.Request.URL.Path),
				attribute.String("http.user_agent", c.Request.UserAgent()),
			),
			trace.WithSpanKind(trace.SpanKindServer),
		}

		ctx, span := Tracer().Start(ctx, spanName, opts...)
		defer span.End()

		// 将 ctx 注入到请求中
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(
			semconv.HTTPStatusCodeKey.Int(status),
		)

		if status >= 400 {
			span.SetAttributes(attribute.String("http.error", fmt.Sprintf("%d", status)))
		}

		// 将 Span 上下文写入响应头（供下游服务使用）
		header := c.Writer.Header()
		propagator.Inject(ctx, propagation.HeaderCarrier(header))
	}
}
