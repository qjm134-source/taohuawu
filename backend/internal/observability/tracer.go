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

// StartSpanWithStartTime 创建一个新的 Span。
// 使用 OpenTelemetry SDK 自动记录开始时间，确保时间一致性。
// 使用方式：
//
//	ctx, span := observability.StartSpanWithStartTime(ctx, "HandleChat")
//	defer observability.EndSpanWithDuration(ctx, span)
func StartSpanWithStartTime(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

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

// EndSpanWithDuration 结束 Span。
// SDK 自动计算并记录耗时，确保与 UI 显示一致。
func EndSpanWithDuration(ctx context.Context, span trace.Span) {
	if span == nil {
		return
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
	return Tracer().Start(ctx, name)
}

// StartChildSpanAt 创建一个子 Span 并使用指定的开始时间。
// 用于记录跨 goroutine 的延迟操作（如等待首 token）。
func StartChildSpanAt(ctx context.Context, name string, startTime time.Time) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithTimestamp(startTime))
}

func EndChildSpan(ctx context.Context, span trace.Span) {
	if span == nil {
		return
	}
	span.End()
}
