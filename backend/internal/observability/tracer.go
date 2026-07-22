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

	// Session 和 User 属性（用于 Langfuse Sessions 和 Users 视图）
	SessionID = attribute.Key("session_id")
	UserID    = attribute.Key("user_id")

	// gen_ai 语义属性常量（遵循 OpenTelemetry Semantic Conventions for LLMs）
	// 参考：https://github.com/open-telemetry/semantic-conventions/blob/main/docs/gen-ai/gen-ai-spans.md
	GenAISystem                  = attribute.Key("gen_ai.system")
	GenAIModelName               = attribute.Key("gen_ai.model.name")
	GenAIModelVersion            = attribute.Key("gen_ai.model.version")
	GenAIRequestID               = attribute.Key("gen_ai.request_id")
	GenAIRequestType             = attribute.Key("gen_ai.request.type")
	GenAIRequestInputTokenCount  = attribute.Key("gen_ai.request.input_token_count")
	GenAIRequestOutputTokenCount = attribute.Key("gen_ai.request.output_token_count")
	GenAIRequestTotalTokenCount  = attribute.Key("gen_ai.request.total_token_count")
	GenAIRequestMaxTokens        = attribute.Key("gen_ai.request.max_tokens")
	GenAIRequestTemperature      = attribute.Key("gen_ai.request.temperature")
	GenAIRequestTopP             = attribute.Key("gen_ai.request.top_p")
	GenAIRequestTopK             = attribute.Key("gen_ai.request.top_k")
	GenAIRequestFrequencyPenalty = attribute.Key("gen_ai.request.frequency_penalty")
	GenAIRequestPresencePenalty  = attribute.Key("gen_ai.request.presence_penalty")
	GenAIRequestStopSequence     = attribute.Key("gen_ai.request.stop_sequence")
	GenAIResponseFinishReason    = attribute.Key("gen_ai.response.finish_reason")
	GenAIResponseLatencyMs       = attribute.Key("gen_ai.response.latency_ms")
	GenAIResponseModel           = attribute.Key("gen_ai.response.model")
	GenAIErrorType               = attribute.Key("gen_ai.error.type")
	GenAIErrorMessage            = attribute.Key("gen_ai.error.message")

	// gen_ai.usage.* 属性（Langfuse 专用，用于成本计算）
	// Langfuse 需要这些属性来显示 token 计数和成本
	GenAIUsageInputTokens  = attribute.Key("gen_ai.usage.input_tokens")
	GenAIUsageOutputTokens = attribute.Key("gen_ai.usage.output_tokens")
	GenAIUsageTotalTokens  = attribute.Key("gen_ai.usage.total_tokens")

	// 消息相关属性
	GenAIMessageRole        = attribute.Key("gen_ai.message.role")
	GenAIMessageContent     = attribute.Key("gen_ai.message.content")
	GenAIMessageContentType = attribute.Key("gen_ai.message.content_type")

	// Langfuse Input/Output 属性（用于映射 Generation 的 Input/Output 字段）
	GenAIPrompt     = attribute.Key("gen_ai.prompt")
	GenAICompletion = attribute.Key("gen_ai.completion")

	// Langfuse 专用属性（优先级最高）
	LangfuseObservationInput  = attribute.Key("langfuse.observation.input")
	LangfuseObservationOutput = attribute.Key("langfuse.observation.output")
	LangfuseObservationType   = attribute.Key("langfuse.observation.type")

	// Langfuse 标签属性
	LangfuseTagFeature = attribute.Key("feature")
	LangfuseTagTenant  = attribute.Key("tenant")
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

func EndChildSpan(ctx context.Context, span trace.Span) {
	if span == nil {
		return
	}
	span.End()
}

// StartLLMSpan 创建一个用于 LLM 调用的 Span，自动设置 gen_ai.* 语义属性。
// Langfuse 会根据这些属性自动映射为 Generation。
func StartLLMSpan(ctx context.Context, modelName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	allAttrs := append([]attribute.KeyValue{
		GenAIModelName.String(modelName),
		GenAIRequestType.String("completion"),
		attribute.String("gen_ai.operation.name", "chat"),
		LangfuseObservationType.String("GENERATION"),
	}, attrs...)
	return Tracer().Start(ctx, "llm.chat", trace.WithAttributes(allAttrs...))
}

// SetSessionID 设置会话 ID，用于 Langfuse Sessions 视图分组对话
func SetSessionID(ctx context.Context, sessionID string) {
	SetAttributes(ctx, SessionID.String(sessionID))
}

// SetUserID 设置用户 ID，用于 Langfuse Users 视图
func SetUserID(ctx context.Context, userID string) {
	SetAttributes(ctx, UserID.String(userID))
}

// SetFeatureTag 设置功能标签，用于 Langfuse Dashboard 过滤
func SetFeatureTag(ctx context.Context, feature string) {
	SetAttributes(ctx, LangfuseTagFeature.String(feature))
}

// SetTenantTag 设置租户标签，用于按租户分析成本和质量
func SetTenantTag(ctx context.Context, tenant string) {
	SetAttributes(ctx, LangfuseTagTenant.String(tenant))
}
