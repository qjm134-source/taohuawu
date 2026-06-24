package observability

import (
	"context"

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
