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

const (
	defaultTemperature         float32 = 0.7
	welcomeMaxTokens           int     = 500
	chatMaxTokens              int     = 300
	defaultModelName                   = "unknown"
	finishReasonStop                   = "stop"
	defaultSimilarityThreshold         = 0.85

	// 上下文构建相关
	tokenThreshold     = 4096
	keepRecentMessages = 6
	minRecentMessages  = 2

	// channel 缓冲
	streamEventBuffer = 100
	cacheHitBuffer    = 2
	statsBufferSize   = 1
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
	llm             *llmCaller
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

// llmCaller 封装主 LLM 适配器与 fallback 适配器的调用逻辑。
type llmCaller struct {
	primary    llm.Adapter
	fallback   llm.Adapter
	llmTimeout time.Duration
	logger     logging.Logger
}

func newLLMCaller(primary, fallback llm.Adapter, llmTimeout time.Duration, logger logging.Logger) *llmCaller {
	return &llmCaller{
		primary:    primary,
		fallback:   fallback,
		llmTimeout: llmTimeout,
		logger:     logger,
	}
}

// chat 先尝试主适配器，失败或不可用时回退到 fallback。
func (c *llmCaller) chat(ctx context.Context, messages []*eino_schema.Message, temperature float32, maxTokens int) (*eino_schema.Message, *llm.ChatUsage, error) {
	if !c.primary.IsHealthy() {
		c.logger.Warn("Primary LLM unhealthy, using fallback")
		return c.fallback.Chat(ctx, messages, llm.WithTemperature(temperature), llm.WithMaxTokens(maxTokens))
	}

	llmCtx, cancel := utils.WithTimeoutFrom(ctx, c.llmTimeout)
	defer cancel()

	msg, usage, err := c.primary.Chat(llmCtx, messages, llm.WithTemperature(temperature), llm.WithMaxTokens(maxTokens))
	if err != nil {
		return c.fallback.Chat(ctx, messages, llm.WithTemperature(temperature), llm.WithMaxTokens(maxTokens))
	}
	return msg, usage, nil
}

// streamChat 先尝试主适配器流式调用，失败或不可用时回退到 fallback。
func (c *llmCaller) streamChat(ctx context.Context, messages []*eino_schema.Message, temperature float32, maxTokens int) (llm.EventStream, error) {
	if !c.primary.IsHealthy() {
		c.logger.Warn("Primary LLM unhealthy, using fallback")
		return c.fallback.StreamChat(ctx, messages, llm.WithTemperature(temperature), llm.WithMaxTokens(maxTokens))
	}

	stream, err := c.primary.StreamChat(ctx, messages, llm.WithTemperature(temperature), llm.WithMaxTokens(maxTokens))
	if err != nil {
		return c.fallback.StreamChat(ctx, messages, llm.WithTemperature(temperature), llm.WithMaxTokens(maxTokens))
	}
	return stream, nil
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
		llm:             newLLMCaller(llmAdapter, fallbackAdapter, config.LLMTimeout, logger),
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

	msg, usage, err := r.llm.chat(ctx, messages, defaultTemperature, welcomeMaxTokens)
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
	if cached, hit := r.lookupCache(ctx, span, cacheKey, message); hit {
		return r.handleCacheHitSync(cached, emotionStr)
	}

	return r.callLLMAndProcess(ctx, span, session, message, emotionStr, cacheKey)
}

func (r *Runtime) lookupCache(ctx context.Context, span trace.Span, cacheKey, message string) (string, bool) {
	if cached, hit := r.checkExactCache(ctx, span, cacheKey); hit {
		return cached, true
	}
	return r.checkSimilarityCache(ctx, span, message)
}

func (r *Runtime) checkExactCache(ctx context.Context, span trace.Span, cacheKey string) (string, bool) {
	_, cacheSpan := observability.StartChildSpan(ctx, "Cache.ExactCheck")
	defer observability.EndChildSpan(ctx, cacheSpan)

	if cached, hit := r.optimizer.GetCache(cacheKey); hit {
		markCacheHit(span)
		observability.CacheHitsTotal.WithLabelValues("exact").Inc()
		return cached, true
	}

	observability.CacheMissesTotal.WithLabelValues("exact").Inc()
	return "", false
}

func (r *Runtime) checkSimilarityCache(ctx context.Context, span trace.Span, message string) (string, bool) {
	_, similaritySpan := observability.StartChildSpan(ctx, "Cache.SimilarityCheck")
	defer observability.EndChildSpan(ctx, similaritySpan)

	if cached, hit := r.optimizer.CheckSimilarity(ctx, message, defaultSimilarityThreshold); hit {
		markCacheHit(span)
		observability.CacheHitsTotal.WithLabelValues("similarity").Inc()
		return cached, true
	}

	observability.CacheMissesTotal.WithLabelValues("similarity").Inc()
	return "", false
}

func (r *Runtime) handleCacheHitSync(cached, emotionStr string) (string, string, *LLMStats, error) {
	observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
	return cached, emotionStr, &LLMStats{CacheHit: true}, nil
}

func (r *Runtime) callLLMAndProcess(ctx context.Context, span trace.Span,
	session *Session, message, emotionStr, cacheKey string) (string, string, *LLMStats, error) {

	messages := r.buildContextMessages(session, message, emotionStr)

	msg, usage, err := r.llm.chat(ctx, messages, defaultTemperature, chatMaxTokens)
	if err != nil {
		r.logger.Error("[HandleChat] All LLM calls failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
		observability.RecordError(span, err)
		return "", "", nil, fmt.Errorf("failed to get response: %w", err)
	}

	return r.processLLMResponse(span, session, message, emotionStr, cacheKey, msg, usage)
}

func (r *Runtime) processLLMResponse(span trace.Span,
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

	span.SetAttributes(buildLLMSpanAttributes(
		usage.Model,
		usage.PromptTokens,
		usage.CompletionTokens,
		usage.TotalTokens,
		stats.Cost,
	)...)

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
	stream, err := r.llm.streamChat(llmCtx, messages, defaultTemperature, chatMaxTokens)
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
	markCacheHit(span)
	observability.AddEvent(ctx, "cache_hit", attribute.String("type", "check"))
	observability.CacheHitsTotal.WithLabelValues("check").Inc()
	observability.EndSpanWithDuration(ctx, span)
	observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()

	eventChan := make(chan *llm.StreamEvent, cacheHitBuffer)
	eventChan <- &llm.StreamEvent{
		Type:    llm.StreamEventTypeChunk,
		Content: cached,
	}
	eventChan <- &llm.StreamEvent{
		Type:       llm.StreamEventTypeAction,
		ActionType: "exit",
	}
	close(eventChan)

	statsChan := make(chan *LLMStats, statsBufferSize)
	statsChan <- &LLMStats{CacheHit: true}
	close(statsChan)

	return eventChan, statsChan, nil
}

func (r *Runtime) buildContextMessagesWithSpan(ctx context.Context, session *Session, message, emotionStr string) []*eino_schema.Message {
	_, span := observability.StartChildSpan(ctx, "Context.Build")
	defer observability.EndChildSpan(ctx, span)
	observability.CacheMissesTotal.WithLabelValues("check").Inc()
	return r.buildContextMessages(session, message, emotionStr)
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

	eventChan := make(chan *llm.StreamEvent, streamEventBuffer)
	statsChan := make(chan *LLMStats, statsBufferSize)

	go func() {
		defer utils.RecoverWithCustomLogger("HandleChatStream", r.logger)
		defer close(eventChan)
		defer observability.EndChildSpan(ctx, llmSpan)
		defer stream.Close()
		defer observability.EndSpanWithDuration(ctx, span)

		r.processStream(ctx, stream, eventChan, statsChan,
			session, message, emotionStr, cacheKey, startTime, span, llmSpan)
	}()

	return eventChan, statsChan, nil
}

func (r *Runtime) processStream(ctx context.Context, stream llm.EventStream,
	eventChan chan<- *llm.StreamEvent, statsChan chan<- *LLMStats,
	session *Session, message, emotionStr, cacheKey string, startTime time.Time, span, llmSpan trace.Span) {

	var fullReply strings.Builder
	stats := &LLMStats{CacheHit: false}

	setSpanInput(span, message)
	finishReason, chunkCount := r.consumeStream(ctx, stream, eventChan, &fullReply, stats)
	r.finalizeStream(ctx, span, llmSpan, eventChan, stats, startTime, chunkCount, finishReason, fullReply.String(), session, message, emotionStr, cacheKey)

	statsChan <- stats
	close(statsChan)
}

func (r *Runtime) consumeStream(ctx context.Context, stream llm.EventStream,
	eventChan chan<- *llm.StreamEvent, fullReply *strings.Builder, stats *LLMStats) (string, int) {

	chunkCount := 0
	var finishReason string
	var streamSpan trace.Span

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

		switch event.Type {
		case llm.StreamEventTypeChunk:
			if chunkCount == 0 {
				_, streamSpan = observability.StartChildSpan(ctx, "LLM.TokenStreaming")
				defer observability.EndChildSpan(ctx, streamSpan)
			}
			chunkCount++
			if reason := r.updateStatsFromChunk(event, fullReply, stats); reason != "" {
				finishReason = reason
			}
			select {
			case eventChan <- event:
			case <-ctx.Done():
				return finishReason, chunkCount
			}
		case llm.StreamEventTypeAction:
			if event.ActionType == "exit" {
				r.updateStatsFromExitEvent(event, stats)
			}
		}
	}
	return finishReason, chunkCount
}

func (r *Runtime) finalizeStream(ctx context.Context, span, llmSpan trace.Span, eventChan chan<- *llm.StreamEvent,
	stats *LLMStats, startTime time.Time, chunkCount int, finishReason, reply string,
	session *Session, message, emotionStr, cacheKey string) {

	r.sendFinishEvent(eventChan, ctx, finishReason, reply != "", stats.Model)
	r.updateLLMStatsAndMetrics(ctx, llmSpan, span, stats, startTime, chunkCount, finishReason)
	r.updateSession(ctx, session, message, emotionStr, reply)
	r.writeCache(ctx, cacheKey, reply)
	setSpanOutput(span, reply)
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
	)
	llmSpan.SetAttributes(buildLLMSpanAttributes(
		model,
		stats.InputTokens,
		stats.OutputTokens,
		stats.TotalTokens,
		stats.Cost,
	)...)

	if model != "unknown" {
		span.SetAttributes(buildLLMSpanAttributes(
			model,
			stats.InputTokens,
			stats.OutputTokens,
			stats.TotalTokens,
			stats.Cost,
		)...)
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

// buildContextMessages 构建 LLM 请求的消息上下文。
// 优先使用 LLM 摘要替代硬截断：当历史消息预估 Token 数超过阈值时，
// 将早期消息压缩为一段摘要文本，保留最近的 N 条原始消息。
func (r *Runtime) buildContextMessages(session *Session, currentMessage string, emotion string) []*eino_schema.Message {
	allMessages := session.GetMessages(0)
	if len(allMessages) == 0 {
		return r.buildInitialMessages(currentMessage, emotion)
	}

	if r.estimateMessagesTokens(allMessages, currentMessage) <= tokenThreshold {
		return r.buildFullHistoryMessages(allMessages, currentMessage, emotion)
	}
	return r.buildSummarizedMessages(allMessages, currentMessage, emotion)
}

func (r *Runtime) buildInitialMessages(currentMessage, emotion string) []*eino_schema.Message {
	return []*eino_schema.Message{
		{Role: eino_schema.System, Content: SystemPrompt},
		{Role: eino_schema.User, Content: fmt.Sprintf("[情绪:%s] %s", emotion, currentMessage)},
	}
}

func (r *Runtime) estimateMessagesTokens(messages []Message, currentMessage string) int {
	summarizer := cost.GetSummarizer()
	total := 0
	for _, msg := range messages {
		total += estimateContentTokens(summarizer, msg.Content)
	}
	total += estimateContentTokens(summarizer, currentMessage)
	return total
}

func estimateContentTokens(summarizer cost.Summarizer, content string) int {
	if summarizer != nil {
		return summarizer.EstimateTokens(content)
	}
	return len([]rune(content)) * 2
}

func (r *Runtime) buildFullHistoryMessages(messages []Message, currentMessage, emotion string) []*eino_schema.Message {
	result := []*eino_schema.Message{{Role: eino_schema.System, Content: SystemPrompt}}
	for _, msg := range messages {
		if msg.Content == "" {
			continue
		}
		result = append(result, &eino_schema.Message{
			Role:    toEinoRole(msg.Role),
			Content: msg.Content,
		})
	}
	result = append(result, &eino_schema.Message{
		Role:    eino_schema.User,
		Content: fmt.Sprintf("[情绪:%s] %s", emotion, currentMessage),
	})
	return result
}

func toEinoRole(role string) eino_schema.RoleType {
	if role == "assistant" {
		return eino_schema.Assistant
	}
	return eino_schema.User
}

func (r *Runtime) buildSummarizedMessages(messages []Message, currentMessage, emotion string) []*eino_schema.Message {
	keepRecent := recentMessageCount(len(messages))
	recentMessages := messages[len(messages)-keepRecent:]
	oldMessages := messages[:len(messages)-keepRecent]

	result := []*eino_schema.Message{{Role: eino_schema.System, Content: SystemPrompt}}
	result = append(result, r.buildSummaryMessage(oldMessages)...)

	for _, msg := range recentMessages {
		if msg.Content == "" {
			continue
		}
		result = append(result, &eino_schema.Message{
			Role:    toEinoRole(msg.Role),
			Content: msg.Content,
		})
	}

	result = append(result, &eino_schema.Message{
		Role:    eino_schema.User,
		Content: fmt.Sprintf("[情绪:%s] %s", emotion, currentMessage),
	})
	return result
}

func recentMessageCount(total int) int {
	if total <= keepRecentMessages {
		count := total / 2
		if count < minRecentMessages {
			return minRecentMessages
		}
		return count
	}
	return keepRecentMessages
}

func (r *Runtime) buildSummaryMessage(oldMessages []Message) []*eino_schema.Message {
	if len(oldMessages) == 0 {
		return nil
	}

	summary := r.summarizeMessages(context.Background(), oldMessages)
	if summary != "" {
		return []*eino_schema.Message{{
			Role:    eino_schema.System,
			Content: "[对话历史摘要] " + summary,
		}}
	}
	return []*eino_schema.Message{{
		Role:    eino_schema.System,
		Content: fmt.Sprintf("[之前有%d条对话，以下是最近的对话内容]", len(oldMessages)),
	}}
}

func (r *Runtime) summarizeMessages(ctx context.Context, oldMessages []Message) string {
	summarizer := cost.GetSummarizer()
	if summarizer == nil {
		return ""
	}

	oldText := messagesToText(oldMessages)
	var summary string
	var err error
	if r.summaryCache != "" {
		summary, err = summarizer.IncrementalSummarize(ctx, r.summaryCache, oldText)
	} else {
		summary, err = summarizer.Summarize(ctx, oldText)
	}
	if err != nil {
		return ""
	}

	r.summaryCache = summary
	return summary
}

// messagesToText 将 Session 消息列表转换为纯文本。
func messagesToText(msgs []Message) string {
	var sb strings.Builder
	for _, msg := range msgs {
		sb.WriteString(rolePrefix(msg.Role))
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

func rolePrefix(role string) string {
	switch role {
	case "assistant":
		return "[助手] "
	case "system":
		return "[系统] "
	default:
		return "[用户] "
	}
}

// markCacheHit 在 span 上标记缓存命中。
func markCacheHit(span trace.Span) {
	span.SetAttributes(attribute.Bool("cache_hit", true))
}

// setSpanInput 在 span 上设置 LLM 输入内容属性（GenAI + Langfuse）。
func setSpanInput(span trace.Span, input string) {
	span.SetAttributes(
		observability.GenAIPrompt.String(input),
		observability.LangfuseObservationInput.String(input),
	)
}

// setSpanOutput 在 span 上设置 LLM 输出内容属性（GenAI + Langfuse）。
func setSpanOutput(span trace.Span, output string) {
	span.SetAttributes(
		observability.GenAICompletion.String(output),
		observability.LangfuseObservationOutput.String(output),
	)
}

// buildLLMSpanAttributes 构建 LLM 调用相关的 OTel span 属性。
// 统一封装 llm.* 前缀属性和 gen_ai.* 语义属性，避免在业务函数中重复大段设置。
func buildLLMSpanAttributes(model string, inputTokens, outputTokens, totalTokens int, costAmount float64) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("llm.model", model),
		attribute.Int("llm.input_tokens", inputTokens),
		attribute.Int("llm.output_tokens", outputTokens),
		attribute.Int("llm.total_tokens", totalTokens),
		attribute.Float64("llm.cost", costAmount),
		observability.GenAIModelName.String(model),
		observability.GenAIRequestInputTokenCount.Int(inputTokens),
		observability.GenAIRequestOutputTokenCount.Int(outputTokens),
		observability.GenAIRequestTotalTokenCount.Int(totalTokens),
		// Langfuse 专用属性，用于成本计算
		observability.GenAIUsageInputTokens.Int(inputTokens),
		observability.GenAIUsageOutputTokens.Int(outputTokens),
		observability.GenAIUsageTotalTokens.Int(totalTokens),
		observability.GenAIUsageCost.Float64(costAmount),
	}
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
