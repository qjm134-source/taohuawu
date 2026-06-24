package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	// TracerName 追踪器名称
	TracerName = "watertown-guide"
)

// Tracer 返回全局 Tracer 实例。
func Tracer() trace.Tracer {
	return otel.Tracer(TracerName)
}

// StartSpan 创建一个新的 Span，自动记录错误。
// 使用方式：
//
//	ctx, span := observability.StartSpan(ctx, "HandleChat")
//	defer span.End()
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// StartSpanWithStartTime 创建一个新的 Span 并记录开始时间，用于后续计算耗时。
// 返回一个包含开始时间的 context，配合 EndSpanWithDuration 使用。
// 使用方式：
//
//	ctx, span := observability.StartSpanWithStartTime(ctx, "HandleChat")
//	defer observability.EndSpanWithDuration(ctx, span)
func StartSpanWithStartTime(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	ctx = context.WithValue(ctx, spanStartTimeKey{}, time.Now())
	return Tracer().Start(ctx, name, opts...)
}

// spanStartTimeKey 用于在 context 中存储 Span 开始时间
type spanStartTimeKey struct{}

// RecordError 在 Span 上记录错误并设置状态。
func RecordError(span trace.Span, err error) {
	if err != nil && span != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// SpanFromContext 从 context 中提取当前 Span（可能为 nil）。
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// AddEvent 在 Span 上添加事件。
func AddEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := SpanFromContext(ctx)
	if span != nil && span.IsRecording() {
		span.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// SetAttributes 在 Span 上设置属性。
func SetAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := SpanFromContext(ctx)
	if span != nil && span.IsRecording() {
		span.SetAttributes(attrs...)
	}
}

// EndSpanWithDuration 结束 Span 并记录耗时（毫秒）。
// 需要配合 StartSpanWithStartTime 使用。
func EndSpanWithDuration(ctx context.Context, span trace.Span) {
	if span == nil {
		return
	}

	// 从 context 中获取开始时间并计算耗时
	if startTime, ok := ctx.Value(spanStartTimeKey{}).(time.Time); ok {
		durationMs := time.Since(startTime).Milliseconds()
		span.SetAttributes(
			attribute.Int64("duration_ms", durationMs),
		)
	}

	span.End()
}

// StartChildSpan 创建一个子 Span 并记录开始时间。
// 返回一个包含开始时间的 context 和 Span，配合 EndChildSpan 使用。
// 使用方式：
//
//	_, childSpan := observability.StartChildSpan(ctx, "Cache.Check")
//	defer observability.EndChildSpan(ctx, childSpan)
func StartChildSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	startTime := time.Now()
	ctx = context.WithValue(ctx, spanStartTimeKey{}, startTime)
	return Tracer().Start(ctx, name)
}

// EndChildSpan 结束子 Span 并记录耗时（毫秒）。
// 需要配合 StartChildSpan 使用。
func EndChildSpan(ctx context.Context, span trace.Span) {
	if span == nil {
		return
	}

	// 从 context 中获取开始时间并计算耗时
	if startTime, ok := ctx.Value(spanStartTimeKey{}).(time.Time); ok {
		durationMs := time.Since(startTime).Milliseconds()
		span.SetAttributes(
			attribute.Int64("duration_ms", durationMs),
		)
	}

	span.End()
}
