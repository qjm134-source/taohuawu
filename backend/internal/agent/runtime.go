package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	eino_schema "github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/watertown/guide/internal/agent/tools"
	"github.com/watertown/guide/internal/config"
	"github.com/watertown/guide/internal/cost"
	"github.com/watertown/guide/internal/emotion"
	"github.com/watertown/guide/internal/llm"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/pkg/logging"
	"github.com/watertown/guide/pkg/utils"
)

// LLMStats LLM 调用统计信息
type LLMStats struct {
	Model        string
	LatencyMs    int64
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Cost         float64
	CacheHit     bool
	ToolsUsed    []string // 本次对话调用的工具名称列表
}

// Runtime Agent 运行时
type Runtime struct {
	llmAdapter      llm.Adapter
	fallbackAdapter llm.Adapter
	toolRegistry    *tools.ToolRegistry
	sessionManager  *SessionManager
	optimizer       *cost.Optimizer
	emotionDetector emotion.Detector
	config          Config
	logger          logging.Logger

	// summaryCache 缓存的对话摘要（增量式更新用）
	summaryCache string

	// inflightWelcome 防止同一个 player 并发触发 HandleWelcome，
	// value 是 chan struct{}，第一个请求 close channel，后续请求等待。
	inflightWelcome sync.Map // playerID → chan struct{}
}

// Config Agent 配置
type Config struct {
	MaxRetries       int
	Timeout          time.Duration
	LLMTimeout       time.Duration
	ToolTimeout      time.Duration
	FallbackResponse config.FallbackResponse
}

// NewRuntime 创建运行时
func NewRuntime(
	llmAdapter, fallbackAdapter llm.Adapter,
	toolRegistry *tools.ToolRegistry,
	sessionManager *SessionManager,
	optimizer *cost.Optimizer,
	emotionDetector emotion.Detector,
	config Config,
	logger logging.Logger,
) *Runtime {
	return &Runtime{
		llmAdapter:      llmAdapter,
		fallbackAdapter: fallbackAdapter,
		toolRegistry:    toolRegistry,
		sessionManager:  sessionManager,
		optimizer:       optimizer,
		emotionDetector: emotionDetector,
		config:          config,
		logger:          logger,
	}
}

// HandleWelcome 处理欢迎
// 同一 player 的并发请求会被合并：只有第一个真正调用 LLM，其余等待并共享缓存结果。
func (r *Runtime) HandleWelcome(ctx context.Context, session *Session) (string, error) {
	// 总超时
	ctx, cancel := utils.WithTimeoutFrom(ctx, r.config.Timeout)
	defer cancel()

	ctx, span := observability.StartSpanWithStartTime(ctx, "Agent.HandleWelcome",
		trace.WithAttributes(
			attribute.String("player_id", session.PlayerID),
			attribute.String("tenant_id", session.TenantID),
		),
	)
	defer observability.EndSpanWithDuration(ctx, span)

	cacheKey := "welcome_" + session.PlayerID

	if cached, hit := r.optimizer.GetCache(cacheKey); hit {
		return cached, nil
	}

	notifyChan := make(chan struct{})
	if actual, loaded := r.inflightWelcome.LoadOrStore(session.PlayerID, notifyChan); loaded {
		select {
		case <-actual.(chan struct{}):
			// 上一个请求完成，从缓存读取
		case <-ctx.Done():
			return "", ctx.Err()
		}
		// 再次检查缓存
		if cached, hit := r.optimizer.GetCache(cacheKey); hit {
			return cached, nil
		}
		// 如果缓存仍然为空（上一个请求失败了），自己重试
		// 清除旧的 inflight 标记，重新进入
		r.inflightWelcome.Delete(session.PlayerID)
		// 递归重试一次（带 guard 防止无限递归）
		return r.handleWelcomeInternal(ctx, session, cacheKey)
	}
	// 确保完成后通知等待者
	defer func() {
		r.inflightWelcome.Delete(session.PlayerID)
		close(notifyChan)
	}()

	return r.handleWelcomeInternal(ctx, session, cacheKey)
}

// handleWelcomeInternal 实际的欢迎消息生成逻辑（不含 inflight guard）。
func (r *Runtime) handleWelcomeInternal(ctx context.Context, session *Session, cacheKey string) (string, error) {
	startTime := time.Now()

	prompt := BuildWelcomePrompt(session.Nickname)
	messages := r.buildWelcomeMessages(prompt)

	msg, usage, err := r.callLLMForWelcome(ctx, messages)
	if err != nil {
		return r.handleWelcomeError(ctx, startTime, cacheKey, session, err)
	}

	return r.processWelcomeResponse(ctx, startTime, cacheKey, session, msg, usage)
}

func (r *Runtime) buildWelcomeMessages(prompt string) []*eino_schema.Message {
	return []*eino_schema.Message{
		{Role: eino_schema.System, Content: SystemPrompt},
		{Role: eino_schema.User, Content: prompt},
	}
}

func (r *Runtime) callLLMForWelcome(ctx context.Context, messages []*eino_schema.Message) (*eino_schema.Message, *llm.ChatUsage, error) {
	if r.llmAdapter.IsHealthy() {
		llmCtx, llmCancel := utils.WithTimeoutFrom(ctx, r.config.LLMTimeout)
		msg, usage, err := r.llmAdapter.Chat(llmCtx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(500))
		llmCancel()

		if err != nil {
			return r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(500))
		}
		return msg, usage, nil
	}

	r.logger.Warn("[HandleWelcome] Primary LLM unhealthy, using fallback")
	return r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(500))
}

func (r *Runtime) handleWelcomeError(ctx context.Context, startTime time.Time,
	cacheKey string, session *Session, err error) (string, error) {

	r.logger.Error("[HandleWelcome] All LLM failed", "error", err)
	observability.AgentRequestsTotal.WithLabelValues("welcome", "error").Inc()

	if r.config.FallbackResponse.Enabled && r.config.FallbackResponse.WelcomeMessage != "" {
		reply := r.config.FallbackResponse.WelcomeMessage
		session.AddMessage("assistant", reply, "neutral", nil)
		r.optimizer.SetCache(cacheKey, reply)
		observability.AgentRequestDuration.WithLabelValues("welcome").Observe(time.Since(startTime).Seconds())
		return reply, nil
	}

	return "", fmt.Errorf("failed to get response: %w", err)
}

func (r *Runtime) processWelcomeResponse(ctx context.Context, startTime time.Time,
	cacheKey string, session *Session, msg *eino_schema.Message, usage *llm.ChatUsage) (string, error) {

	reply := msg.Content
	session.AddMessage("assistant", reply, "neutral", nil)

	r.recordWelcomeMetrics(startTime, usage)
	r.optimizer.SetCache(cacheKey, reply)

	return reply, nil
}

func (r *Runtime) recordWelcomeMetrics(startTime time.Time, usage *llm.ChatUsage) {
	model := usage.Model
	if model == "" {
		model = "unknown"
	}
	llmCost := cost.CalculateCost(model, usage.PromptTokens, usage.CompletionTokens)

	r.recordLLMMetrics(model, "success", time.Since(startTime).Seconds(),
		usage.PromptTokens, usage.CompletionTokens, llmCost)
	observability.AgentRequestsTotal.WithLabelValues("welcome", "success").Inc()
	observability.AgentRequestDuration.WithLabelValues("welcome").Observe(time.Since(startTime).Seconds())
}

// HandleChat 处理聊天（非流式）
func (r *Runtime) HandleChat(ctx context.Context, session *Session, message string) (string, string, *LLMStats, error) {

	ctx, cancel := utils.WithTimeoutFrom(ctx, r.config.Timeout)
	defer cancel()

	ctx = context.WithValue(ctx, "session_id", session.ID)

	ctx, span := observability.StartSpanWithStartTime(ctx, "Agent.HandleChat",
		trace.WithAttributes(
			observability.SessionID.String(session.ID),
			observability.UserID.String(session.PlayerID),
			observability.LangfuseTagTenant.String(session.TenantID),
			observability.LangfuseTagFeature.String("chat"),
		),
	)
	defer observability.EndSpanWithDuration(ctx, span)

	emotionStr := r.detectEmotion(ctx, message)

	cacheKey := session.ID + "_" + message
	if cached, hit := r.checkExactCache(ctx, span, cacheKey); hit {
		return r.handleCacheHitSync(ctx, cached, emotionStr)
	}

	if cached, hit := r.checkSimilarityCache(ctx, span, message); hit {
		return r.handleCacheHitSync(ctx, cached, emotionStr)
	}

	return r.callLLMAndProcess(ctx, span, session, message, emotionStr, cacheKey)
}

func (r *Runtime) checkExactCache(ctx context.Context, span trace.Span, cacheKey string) (string, bool) {
	_, cacheSpan := observability.StartChildSpan(ctx, "Cache.ExactCheck")
	defer observability.EndChildSpan(ctx, cacheSpan)

	if cached, hit := r.optimizer.GetCache(cacheKey); hit {
		span.SetAttributes(attribute.Bool("cache_hit", true))
		observability.CacheHitsTotal.WithLabelValues("exact").Inc()
		r.recordCacheHit()
		return cached, true
	}

	observability.CacheMissesTotal.WithLabelValues("exact").Inc()
	r.recordCacheMiss()
	return "", false
}

func (r *Runtime) checkSimilarityCache(ctx context.Context, span trace.Span, message string) (string, bool) {
	_, similaritySpan := observability.StartChildSpan(ctx, "Cache.SimilarityCheck")
	defer observability.EndChildSpan(ctx, similaritySpan)

	if cached, hit := r.optimizer.CheckSimilarity(ctx, message, 0.85); hit {
		span.SetAttributes(attribute.Bool("cache_hit", true))
		observability.CacheHitsTotal.WithLabelValues("similarity").Inc()
		r.recordCacheHit()
		return cached, true
	}

	observability.CacheMissesTotal.WithLabelValues("similarity").Inc()
	r.recordCacheMiss()
	return "", false
}

func (r *Runtime) handleCacheHitSync(ctx context.Context, cached, emotionStr string) (string, string, *LLMStats, error) {
	observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
	return cached, emotionStr, &LLMStats{CacheHit: true}, nil
}

func (r *Runtime) callLLMAndProcess(ctx context.Context, span trace.Span,
	session *Session, message, emotionStr, cacheKey string) (string, string, *LLMStats, error) {

	messages := r.buildContextMessages(session, message, emotionStr)

	msg, usage, err := r.callLLM(ctx, messages)
	if err != nil {
		r.logger.Error("[HandleChat] All LLM calls failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
		observability.RecordError(span, err)
		return "", "", nil, fmt.Errorf("failed to get response: %w", err)
	}

	return r.processLLMResponse(ctx, span, session, message, emotionStr, cacheKey, msg, usage)
}

func (r *Runtime) callLLM(ctx context.Context, messages []*eino_schema.Message) (*eino_schema.Message, *llm.ChatUsage, error) {
	startTime := time.Now()

	if r.llmAdapter.IsHealthy() {
		llmCtx, llmCancel := utils.WithTimeoutFrom(ctx, r.config.LLMTimeout)
		msg, usage, err := r.llmAdapter.Chat(llmCtx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		llmCancel()

		if err != nil {
			return r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		}
		_ = startTime
		return msg, usage, nil
	}

	r.logger.Warn("[HandleChat] Primary LLM unhealthy, using fallback")
	return r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
}

func (r *Runtime) processLLMResponse(ctx context.Context, span trace.Span,
	session *Session, message, emotionStr, cacheKey string, msg *eino_schema.Message, usage *llm.ChatUsage) (string, string, *LLMStats, error) {

	startTime := time.Now()

	reply := strings.TrimSpace(msg.Content)

	stats := &LLMStats{
		Model:        usage.Model,
		LatencyMs:    time.Since(startTime).Milliseconds(),
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
		Cost:         cost.CalculateCost(usage.Model, usage.PromptTokens, usage.CompletionTokens),
		CacheHit:     false,
	}

	r.recordLLMMetrics(usage.Model, "success", time.Since(startTime).Seconds(),
		usage.PromptTokens, usage.CompletionTokens, stats.Cost)
	observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
	observability.AgentRequestDuration.WithLabelValues("chat").Observe(time.Since(startTime).Seconds())

	span.SetAttributes(
		attribute.String("llm.model", usage.Model),
		attribute.Int("llm.input_tokens", usage.PromptTokens),
		attribute.Int("llm.output_tokens", usage.CompletionTokens),
		attribute.Int("llm.total_tokens", usage.TotalTokens),
		attribute.Float64("llm.cost", stats.Cost),
		observability.GenAIModelName.String(usage.Model),
		observability.GenAIRequestInputTokenCount.Int(usage.PromptTokens),
		observability.GenAIRequestOutputTokenCount.Int(usage.CompletionTokens),
		observability.GenAIRequestTotalTokenCount.Int(usage.TotalTokens),
		// Langfuse 专用属性，用于成本计算
		observability.GenAIUsageInputTokens.Int(usage.PromptTokens),
		observability.GenAIUsageOutputTokens.Int(usage.CompletionTokens),
		observability.GenAIUsageTotalTokens.Int(usage.TotalTokens),
		observability.GenAIUsageCost.Float64(stats.Cost),
	)

	session.AddMessage("user", message, emotionStr, nil)
	if reply != "" {
		session.AddMessage("assistant", reply, emotionStr, nil)
	}

	r.optimizer.SetCache(cacheKey, reply)

	return reply, emotionStr, stats, nil
}

// HandleChatStream 处理聊天（流式）
func (r *Runtime) HandleChatStream(ctx context.Context, session *Session, message string) (<-chan *llm.StreamEvent, <-chan *LLMStats, error) {
	startTime := time.Now()

	ctx = context.WithValue(ctx, "session_id", session.ID)

	ctx, span := observability.StartSpanWithStartTime(ctx, "Agent.HandleChatStream",
		trace.WithAttributes(
			observability.SessionID.String(session.ID),
			observability.UserID.String(session.PlayerID),
			observability.LangfuseTagTenant.String(session.TenantID),
			observability.LangfuseTagFeature.String("chat"),
		),
	)

	emotionStr := r.detectEmotion(ctx, message)

	cacheKey := session.ID + "_" + message
	if cached, hit := r.checkCache(ctx, cacheKey); hit && cached != "" {
		return r.handleCacheHit(ctx, span, cached)
	}

	messages := r.buildContextMessagesWithSpan(ctx, session, message, emotionStr)

	llmCtx, llmSpan := observability.StartChildSpan(ctx, "LLM.StreamChat")
	stream, err := r.callLLMStream(llmCtx, messages)
	if err != nil {
		return r.handleLLMError(ctx, span, llmSpan, err)
	}

	return r.processStreamAsync(ctx, span, llmSpan,
		stream, session, message, emotionStr, cacheKey, startTime)
}

func (r *Runtime) detectEmotion(ctx context.Context, message string) string {
	_, span := observability.StartChildSpan(ctx, "Emotion.Detect")
	defer observability.EndChildSpan(ctx, span)
	em := r.emotionDetector.Detect(message)
	return string(em)
}

func (r *Runtime) checkCache(ctx context.Context, cacheKey string) (string, bool) {
	_, span := observability.StartChildSpan(ctx, "Cache.Check")
	defer observability.EndChildSpan(ctx, span)
	return r.optimizer.GetCache(cacheKey)
}

func (r *Runtime) handleCacheHit(ctx context.Context, span trace.Span, cached string) (<-chan *llm.StreamEvent, <-chan *LLMStats, error) {
	span.SetAttributes(attribute.Bool("cache_hit", true))
	observability.AddEvent(ctx, "cache_hit", attribute.String("type", "check"))
	observability.CacheHitsTotal.WithLabelValues("check").Inc()
	r.recordCacheHit()
	observability.EndSpanWithDuration(ctx, span)
	observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()

	eventChan := make(chan *llm.StreamEvent, 2)
	eventChan <- &llm.StreamEvent{
		Type:    llm.StreamEventTypeChunk,
		Content: cached,
	}
	eventChan <- &llm.StreamEvent{
		Type:       llm.StreamEventTypeAction,
		ActionType: "exit",
	}
	close(eventChan)

	statsChan := make(chan *LLMStats, 1)
	statsChan <- &LLMStats{CacheHit: true}
	close(statsChan)

	return eventChan, statsChan, nil
}

func (r *Runtime) buildContextMessagesWithSpan(ctx context.Context, session *Session, message, emotionStr string) []*eino_schema.Message {
	_, span := observability.StartChildSpan(ctx, "Context.Build")
	defer observability.EndChildSpan(ctx, span)
	observability.CacheMissesTotal.WithLabelValues("check").Inc()
	r.recordCacheMiss()
	return r.buildContextMessages(session, message, emotionStr)
}

func (r *Runtime) callLLMStream(ctx context.Context, messages []*eino_schema.Message) (llm.EventStream, error) {
	isHealthy := r.llmAdapter.IsHealthy()

	if isHealthy {
		stream, err := r.llmAdapter.StreamChat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		if err != nil {
			return r.fallbackAdapter.StreamChat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		}
		return stream, nil
	}

	r.logger.Warn("[HandleChatStream] Primary LLM unhealthy, using fallback")
	return r.fallbackAdapter.StreamChat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
}

func (r *Runtime) handleLLMError(ctx context.Context, span, llmSpan trace.Span, err error) (<-chan *llm.StreamEvent, <-chan *LLMStats, error) {
	r.logger.Error("[HandleChatStream] All LLM stream calls failed", "error", err)
	observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
	observability.RecordError(span, err)
	observability.EndSpanWithDuration(ctx, span)
	observability.EndChildSpan(ctx, llmSpan)
	return nil, nil, fmt.Errorf("failed to get stream response: %w", err)
}

func (r *Runtime) processStreamAsync(ctx context.Context, span, llmSpan trace.Span,
	stream llm.EventStream, session *Session, message, emotionStr, cacheKey string, startTime time.Time) (<-chan *llm.StreamEvent, <-chan *LLMStats, error) {

	eventChan := make(chan *llm.StreamEvent, 100)
	statsChan := make(chan *LLMStats, 1)

	go func() {
		defer utils.RecoverWithCustomLogger("HandleChatStream", r.logger)
		defer close(eventChan)
		defer observability.EndChildSpan(ctx, llmSpan)
		defer stream.Close()
		defer observability.EndSpanWithDuration(ctx, span)

		r.processStreamEvents(ctx, stream, eventChan, statsChan,
			session, message, emotionStr, cacheKey, startTime, span, llmSpan)
	}()

	return eventChan, statsChan, nil
}

func (r *Runtime) processStreamEvents(ctx context.Context, stream llm.EventStream,
	eventChan chan<- *llm.StreamEvent, statsChan chan<- *LLMStats,
	session *Session, message, emotionStr, cacheKey string, startTime time.Time, span, llmSpan trace.Span) {

	var fullReply strings.Builder
	stats := &LLMStats{CacheHit: false}

	chunkCount := 0
	var finishReason string
	var streamSpan trace.Span

	span.SetAttributes(observability.GenAIPrompt.String(message))
	span.SetAttributes(observability.LangfuseObservationInput.String(message))

	for {
		event, err := stream.Recv()

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			r.logger.Error("[HandleChatStream] Stream recv error", "error", err)
			_, errorSpan := observability.StartChildSpan(ctx, "LLM.StreamError")
			errorSpan.RecordError(err)
			observability.EndChildSpan(ctx, errorSpan)
			break
		}

		if event == nil {
			continue
		}

		if event.Type == llm.StreamEventTypeChunk {
			if chunkCount == 0 {
				_, streamSpan = observability.StartChildSpan(ctx, "LLM.TokenStreaming")
				defer observability.EndChildSpan(ctx, streamSpan)
			}
			chunkCount++

			if reason := r.updateStatsFromChunk(event, &fullReply, stats); reason != "" {
				finishReason = reason
			}

			select {
			case eventChan <- event:
			case <-ctx.Done():
				return
			}
		} else if event.Type == llm.StreamEventTypeAction && event.ActionType == "exit" {
			r.updateStatsFromExitEvent(event, stats)
		}
	}

	r.sendFinishEvent(eventChan, ctx, finishReason, fullReply.Len() > 0, stats.Model)
	r.updateLLMStatsAndMetrics(ctx, llmSpan, span, stats, startTime, chunkCount, finishReason)
	r.updateSession(ctx, session, message, emotionStr, fullReply.String())
	r.writeCache(ctx, cacheKey, fullReply.String())

	span.SetAttributes(observability.GenAICompletion.String(fullReply.String()))
	span.SetAttributes(observability.LangfuseObservationOutput.String(fullReply.String()))

	statsChan <- stats
	close(statsChan)

}

func (r *Runtime) updateStatsFromChunk(event *llm.StreamEvent, fullReply *strings.Builder, stats *LLMStats) string {
	if event.Content != "" {
		fullReply.WriteString(event.Content)
	}

	if event.Model != "" && stats.Model == "" {
		stats.Model = event.Model
	}

	if event.Usage != nil && event.Usage.TotalTokens > 0 {
		stats.InputTokens = event.Usage.PromptTokens
		stats.OutputTokens = event.Usage.CompletionTokens
		stats.TotalTokens = event.Usage.TotalTokens
	}

	if len(event.ToolCalls) > 0 && len(stats.ToolsUsed) == 0 {
		var toolNames []string
		for _, tc := range event.ToolCalls {
			toolNames = append(toolNames, tc.ToolName)
		}
		stats.ToolsUsed = toolNames
	}

	return event.FinishReason
}

func (r *Runtime) updateStatsFromExitEvent(event *llm.StreamEvent, stats *LLMStats) {
	if event.Model != "" && stats.Model == "" {
		stats.Model = event.Model
	}

	if event.Usage != nil && event.Usage.TotalTokens > 0 {
		stats.InputTokens = event.Usage.PromptTokens
		stats.OutputTokens = event.Usage.CompletionTokens
		stats.TotalTokens = event.Usage.TotalTokens
	}
}

func (r *Runtime) sendFinishEvent(eventChan chan<- *llm.StreamEvent, ctx context.Context, finishReason string, hasReply bool, model string) {
	if finishReason == "" && hasReply {
		finishReason = "stop"
	}

	if finishReason != "" {
		select {
		case eventChan <- &llm.StreamEvent{
			Type:         llm.StreamEventTypeChunk,
			Content:      "",
			FinishReason: finishReason,
			Model:        model,
		}:
		case <-ctx.Done():
		}
	}
}

func (r *Runtime) updateLLMStatsAndMetrics(llmCtx context.Context, llmSpan, span trace.Span,
	stats *LLMStats, startTime time.Time, chunkCount int, finishReason string) {

	_, postSpan := observability.StartChildSpan(llmCtx, "LLM.StatsAndMetrics")
	defer postSpan.End()

	stats.LatencyMs = time.Since(startTime).Milliseconds()
	stats.Cost = cost.CalculateCost(stats.Model, stats.InputTokens, stats.OutputTokens)

	model := stats.Model
	if model == "" {
		model = "unknown"
	}

	r.recordLLMMetrics(model, "success", time.Since(startTime).Seconds(),
		stats.InputTokens, stats.OutputTokens, stats.Cost)
	observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
	observability.AgentRequestDuration.WithLabelValues("chat").Observe(time.Since(startTime).Seconds())

	llmSpan.SetAttributes(
		attribute.String("model", model),
		attribute.Int("input_tokens", stats.InputTokens),
		attribute.Int("output_tokens", stats.OutputTokens),
		attribute.Int("total_tokens", stats.TotalTokens),
		attribute.Float64("cost", stats.Cost),
		attribute.Int64("ttft_ms", 0),
		attribute.Int("chunk_count", chunkCount),
		attribute.String("finish_reason", finishReason),
		observability.GenAIModelName.String(model),
		observability.GenAIRequestInputTokenCount.Int(stats.InputTokens),
		observability.GenAIRequestOutputTokenCount.Int(stats.OutputTokens),
		observability.GenAIRequestTotalTokenCount.Int(stats.TotalTokens),
		// Langfuse 专用属性，用于成本计算
		observability.GenAIUsageInputTokens.Int(stats.InputTokens),
		observability.GenAIUsageOutputTokens.Int(stats.OutputTokens),
		observability.GenAIUsageTotalTokens.Int(stats.TotalTokens),
		observability.GenAIUsageCost.Float64(stats.Cost),
	)

	if model != "unknown" {
		span.SetAttributes(
			attribute.String("llm.model", model),
			attribute.Int("llm.input_tokens", stats.InputTokens),
			attribute.Int("llm.output_tokens", stats.OutputTokens),
			attribute.Int("llm.total_tokens", stats.TotalTokens),
			attribute.Float64("llm.cost", stats.Cost),
			observability.GenAIModelName.String(model),
			observability.GenAIRequestInputTokenCount.Int(stats.InputTokens),
			observability.GenAIRequestOutputTokenCount.Int(stats.OutputTokens),
			observability.GenAIRequestTotalTokenCount.Int(stats.TotalTokens),
			// Langfuse 专用属性，用于成本计算
			observability.GenAIUsageInputTokens.Int(stats.InputTokens),
			observability.GenAIUsageOutputTokens.Int(stats.OutputTokens),
			observability.GenAIUsageTotalTokens.Int(stats.TotalTokens),
			observability.GenAIUsageCost.Float64(stats.Cost),
		)
	}
}

func (r *Runtime) updateSession(llmCtx context.Context, session *Session, message, emotionStr, reply string) {
	_, span := observability.StartChildSpan(llmCtx, "LLM.SessionUpdate")
	defer span.End()

	session.AddMessage("user", message, emotionStr, nil)
	if reply != "" {
		session.AddMessage("assistant", reply, emotionStr, nil)
	}
	span.SetAttributes(
		attribute.Int("user_msg_len", len(message)),
		attribute.Int("assistant_msg_len", len(reply)))
}

func (r *Runtime) writeCache(llmCtx context.Context, cacheKey, value string) {
	if value == "" {
		return
	}
	_, span := observability.StartChildSpan(llmCtx, "LLM.CacheWrite")
	defer span.End()

	r.optimizer.SetCache(cacheKey, value)
	span.SetAttributes(
		attribute.String("key", cacheKey),
		attribute.Int("value_len", len(value)))
}

// handleNonStreamChat 处理非流式调用，并模拟流式响应
func (r *Runtime) handleNonStreamChat(ctx context.Context, messages []*eino_schema.Message, contentChan chan string, fullReply *strings.Builder, stats *LLMStats) {
	msg, usage, err := r.callLLMNonStreaming(ctx, messages)
	if err != nil || msg == nil {
		return
	}

	r.updateStatsFromUsage(stats, usage)
	r.streamContentToChan(contentChan, fullReply, msg.Content, stats)
}

func (r *Runtime) callLLMNonStreaming(ctx context.Context, messages []*eino_schema.Message) (*eino_schema.Message, *llm.ChatUsage, error) {
	if r.llmAdapter.IsHealthy() {
		msg, usage, err := r.llmAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		if err != nil {
			return r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		}
		return msg, usage, nil
	}

	return r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
}

func (r *Runtime) updateStatsFromUsage(stats *LLMStats, usage *llm.ChatUsage) {
	if usage == nil {
		return
	}
	stats.Model = usage.Model
	stats.InputTokens = usage.PromptTokens
	stats.OutputTokens = usage.CompletionTokens
	stats.TotalTokens = usage.TotalTokens
}

func (r *Runtime) streamContentToChan(contentChan chan string, fullReply *strings.Builder, content string, stats *LLMStats) {
	if content != "" {
		for _, char := range content {
			contentChan <- string(char)
			fullReply.WriteRune(char)
		}
	} else {
		contentChan <- ""
	}
}

// buildContextMessages 构建 LLM 请求的消息上下文。
// 优先使用 LLM 摘要替代硬截断：当历史消息预估 Token 数超过阈值时，
// 将早期消息压缩为一段摘要文本，保留最近的 N 条原始消息。
func (r *Runtime) buildContextMessages(session *Session, currentMessage string, emotion string) []*eino_schema.Message {
	allMessages := session.GetMessages(0)

	if len(allMessages) == 0 {
		return []*eino_schema.Message{
			{Role: eino_schema.System, Content: SystemPrompt},
			{Role: eino_schema.User, Content: fmt.Sprintf("[情绪:%s] %s", emotion, currentMessage)},
		}
	}

	summarizer := cost.GetSummarizer()

	totalTokens := 0
	for _, msg := range allMessages {
		if summarizer != nil {
			totalTokens += summarizer.EstimateTokens(msg.Content)
		} else {
			totalTokens += len([]rune(msg.Content)) * 2
		}
	}
	if summarizer != nil {
		totalTokens += summarizer.EstimateTokens(currentMessage)
	} else {
		totalTokens += len([]rune(currentMessage)) * 2
	}

	const tokenThreshold = 4096

	if totalTokens <= tokenThreshold {
		messages := []*eino_schema.Message{{Role: eino_schema.System, Content: SystemPrompt}}
		for _, msg := range allMessages {
			if msg.Content == "" {
				continue
			}
			role := eino_schema.User
			if msg.Role == "assistant" {
				role = eino_schema.Assistant
			}
			messages = append(messages, &eino_schema.Message{
				Role:    role,
				Content: msg.Content,
			})
		}
		messages = append(messages, &eino_schema.Message{
			Role:    eino_schema.User,
			Content: fmt.Sprintf("[情绪:%s] %s", emotion, currentMessage),
		})
		return messages
	}

	keepRecent := 6
	if len(allMessages) <= keepRecent {
		keepRecent = len(allMessages) / 2
		if keepRecent < 2 {
			keepRecent = 2
		}
	}

	recentMessages := allMessages[len(allMessages)-keepRecent:]
	oldMessages := allMessages[:len(allMessages)-keepRecent]

	messages := []*eino_schema.Message{{Role: eino_schema.System, Content: SystemPrompt}}

	if summarizer != nil && len(oldMessages) > 0 {
		oldText := messagesToText(oldMessages)
		existingSummary := r.summaryCache
		var newSummary string
		var err error

		if existingSummary != "" {
			newSummary, err = summarizer.IncrementalSummarize(context.Background(), existingSummary, oldText)
		} else {
			newSummary, err = summarizer.Summarize(context.Background(), oldText)
		}

		if err == nil && newSummary != "" {
			r.summaryCache = newSummary
			messages = append(messages, &eino_schema.Message{
				Role:    eino_schema.System,
				Content: "[对话历史摘要] " + newSummary,
			})
		} else {
			messages = append(messages, &eino_schema.Message{
				Role:    eino_schema.System,
				Content: fmt.Sprintf("[之前有%d条对话，以下是最近的对话内容]", len(oldMessages)),
			})
		}
	} else if len(oldMessages) > 0 {
		messages = append(messages, &eino_schema.Message{
			Role:    eino_schema.System,
			Content: fmt.Sprintf("[之前有%d条对话，以下是最近的对话内容]", len(oldMessages)),
		})
	}

	for _, msg := range recentMessages {
		if msg.Content == "" {
			continue
		}
		role := eino_schema.User
		if msg.Role == "assistant" {
			role = eino_schema.Assistant
		}
		messages = append(messages, &eino_schema.Message{
			Role:    role,
			Content: msg.Content,
		})
	}

	messages = append(messages, &eino_schema.Message{
		Role:    eino_schema.User,
		Content: fmt.Sprintf("[情绪:%s] %s", emotion, currentMessage),
	})

	return messages
}

// messagesToText 将 Session 消息列表转换为纯文本。
func messagesToText(msgs []Message) string {
	var sb strings.Builder
	for _, msg := range msgs {
		prefix := "[用户] "
		if msg.Role == "assistant" {
			prefix = "[助手] "
		} else if msg.Role == "system" {
			prefix = "[系统] "
		}
		sb.WriteString(prefix)
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// recordLLMMetrics 记录 LLM 相关的 Prometheus 指标。
func (r *Runtime) recordLLMMetrics(model, status string, durationSec float64, inputTokens, outputTokens int, costAmount float64) {
	if model == "" {
		model = "unknown"
	}
	observability.LLMRequestsTotal.WithLabelValues(model, status).Inc()
	observability.LLMRequestDuration.WithLabelValues(model).Observe(durationSec)
	observability.LLMRequestTokens.WithLabelValues(model).Add(float64(inputTokens))
	observability.LLMCompletionTokens.WithLabelValues(model).Add(float64(outputTokens))
	observability.CostTotal.WithLabelValues(model).Add(costAmount)
}

// recordCacheHit 记录缓存命中（仅用于日志，指标通过 CacheHitsTotal Counter 记录）
func (r *Runtime) recordCacheHit() {
}

// recordCacheMiss 记录缓存未命中（仅用于日志，指标通过 CacheMissesTotal Counter 记录）
func (r *Runtime) recordCacheMiss() {
}

// GetSession 获取会话
func (r *Runtime) GetSession(playerID, tenantID string) *Session {
	return r.sessionManager.GetOrCreate(playerID, tenantID)
}

// MarkVisited 标记已访问
func (r *Runtime) MarkVisited(sessionID string) {
	if session, ok := r.sessionManager.Get(sessionID); ok {
		session.MarkVisited()
	}
}

// UpdateNickname 更新昵称
func (r *Runtime) UpdateNickname(sessionID, nickname string) {
	if session, ok := r.sessionManager.Get(sessionID); ok {
		session.UpdateNickname(nickname)
	}
}
