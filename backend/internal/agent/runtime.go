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

	r.logger.Info("[HandleWelcome] Starting", "playerId", session.PlayerID)

	cacheKey := "welcome_" + session.PlayerID

	// 检查缓存
	if cached, hit := r.optimizer.GetCache(cacheKey); hit {
		r.logger.Info("[HandleWelcome] Cache hit", "playerId", session.PlayerID)
		return cached, nil
	}

	// 并发防护：同一 player 只允许一个 HandleWelcome 在飞
	notifyChan := make(chan struct{})
	if actual, loaded := r.inflightWelcome.LoadOrStore(session.PlayerID, notifyChan); loaded {
		// 已有正在进行的请求，等待它完成
		r.logger.Info("[HandleWelcome] Waiting for inflight request", "playerId", session.PlayerID)
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

	langfuseTrace := observability.StartLLMTrace("welcome", session.PlayerID, session.TenantID)

	prompt := BuildWelcomePrompt(session.Nickname)

	messages := []*eino_schema.Message{
		{Role: eino_schema.System, Content: SystemPrompt},
		{Role: eino_schema.User, Content: prompt},
	}

	var msg *eino_schema.Message
	var usage *llm.ChatUsage
	var err error

	if r.llmAdapter.IsHealthy() {
		llmCtx, llmCancel := utils.WithTimeoutFrom(ctx, r.config.LLMTimeout)
		msg, usage, err = r.llmAdapter.Chat(llmCtx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(500))
		llmCancel()

		if err != nil {
			r.logger.Error("[HandleWelcome] Primary LLM failed", "error", err)
			r.logger.Info("[HandleWelcome] Trying fallback adapter")
			msg, usage, err = r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(500))
		}
	} else {
		r.logger.Warn("[HandleWelcome] Primary LLM unhealthy, using fallback")
		msg, usage, err = r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(500))
	}

	if err != nil {
		r.logger.Error("[HandleWelcome] All LLM failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("welcome", "error").Inc()
		langfuseTrace.End()

		if r.config.FallbackResponse.Enabled && r.config.FallbackResponse.WelcomeMessage != "" {
			r.logger.Info("[HandleWelcome] Using fallback response", "message", r.config.FallbackResponse.WelcomeMessage)
			reply := r.config.FallbackResponse.WelcomeMessage
			session.AddMessage("assistant", reply, "neutral", nil)
			r.optimizer.SetCache(cacheKey, reply)
			observability.AgentRequestDuration.WithLabelValues("welcome").Observe(time.Since(startTime).Seconds())
			return reply, nil
		}

		return "", fmt.Errorf("failed to get response: %w", err)
	}

	reply := msg.Content
	session.AddMessage("assistant", reply, "neutral", nil)

	model := usage.Model
	if model == "" {
		model = "unknown"
	}
	llmCost := cost.CalculateCost(model, usage.PromptTokens, usage.CompletionTokens)

	observability.LLMRequestsTotal.WithLabelValues(model, "success").Inc()
	observability.LLMRequestDuration.WithLabelValues(model).Observe(time.Since(startTime).Seconds())
	observability.LLMRequestTokens.WithLabelValues(model).Add(float64(usage.PromptTokens))
	observability.LLMCompletionTokens.WithLabelValues(model).Add(float64(usage.CompletionTokens))
	observability.CostTotal.WithLabelValues(model).Add(llmCost)
	observability.AgentRequestsTotal.WithLabelValues("welcome", "success").Inc()
	observability.AgentRequestDuration.WithLabelValues("welcome").Observe(time.Since(startTime).Seconds())

	langfuseTrace.End()

	r.optimizer.SetCache(cacheKey, reply)

	return reply, nil
}

// HandleChat 处理聊天（非流式）
func (r *Runtime) HandleChat(ctx context.Context, session *Session, message string) (string, string, *LLMStats, error) {
	r.logger.Info("[HandleChat] Start", "sessionId", session.ID, "message", message)

	ctx, cancel := utils.WithTimeoutFrom(ctx, r.config.Timeout)
	defer cancel()

	ctx, span := observability.StartSpanWithStartTime(ctx, "Agent.HandleChat",
		trace.WithAttributes(
			attribute.String("session_id", session.ID),
			attribute.String("player_id", session.PlayerID),
			attribute.String("tenant_id", session.TenantID),
		),
	)
	defer observability.EndSpanWithDuration(ctx, span)

	// Langfuse trace
	langfuseTrace := observability.StartLLMTrace("chat", session.PlayerID, session.TenantID)

	// 初始化统计信息
	stats := &LLMStats{}

	// 检测情绪
	em := r.emotionDetector.Detect(message)
	emotionStr := string(em)

	// 检查精确缓存
	cacheKey := session.ID + "_" + message
	_, cacheSpan := observability.StartChildSpan(ctx, "Cache.ExactCheck")
	if cached, hit := r.optimizer.GetCache(cacheKey); hit {
		observability.EndChildSpan(ctx, cacheSpan)
		r.logger.Info("[HandleChat] Cache hit", "sessionId", session.ID, "cached_length", len(cached))
		span.SetAttributes(attribute.Bool("cache_hit", true))
		session.AddMessage("assistant", cached, emotionStr, nil)
		stats.CacheHit = true
		observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
		langfuseTrace.End()
		return cached, emotionStr, stats, nil
	}
	observability.EndChildSpan(ctx, cacheSpan)

	// 检查语义缓存（相似问题匹配）
	_, similaritySpan := observability.StartChildSpan(ctx, "Cache.SimilarityCheck")
	if cached, hit := r.optimizer.CheckSimilarity(ctx, message, 0.85); hit {
		observability.EndChildSpan(ctx, similaritySpan)
		r.logger.Info("[HandleChat] Semantic cache hit", "sessionId", session.ID, "cached_length", len(cached))
		span.SetAttributes(attribute.Bool("cache_hit", true))
		session.AddMessage("assistant", cached, emotionStr, nil)
		stats.CacheHit = true
		observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
		langfuseTrace.End()
		return cached, emotionStr, stats, nil
	}
	observability.EndChildSpan(ctx, similaritySpan)

	// 构建上下文消息（含摘要压缩）
	messages := r.buildContextMessages(session, message)

	var msg *eino_schema.Message
	var usage *llm.ChatUsage
	var err error
	startTime := time.Now()

	if r.llmAdapter.IsHealthy() {
		llmCtx, llmCancel := utils.WithTimeoutFrom(ctx, r.config.LLMTimeout)
		msg, usage, err = r.llmAdapter.Chat(llmCtx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		llmCancel()

		if err != nil {
			r.logger.Error("[HandleChat] Primary LLM failed", "error", err)
			r.logger.Info("[HandleChat] Trying fallback adapter")
			msg, usage, err = r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		}
	} else {
		r.logger.Warn("[HandleChat] Primary LLM unhealthy, using fallback")
		msg, usage, err = r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
	}

	if err != nil {
		r.logger.Error("[HandleChat] All LLM calls failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
		observability.RecordError(span, err)
		langfuseTrace.End()
		return "", "", nil, fmt.Errorf("failed to get response: %w", err)
	}

	reply := strings.TrimSpace(msg.Content)

	stats.Model = usage.Model
	stats.LatencyMs = time.Since(startTime).Milliseconds()
	stats.InputTokens = usage.PromptTokens
	stats.OutputTokens = usage.CompletionTokens
	stats.TotalTokens = usage.TotalTokens
	stats.Cost = cost.CalculateCost(usage.Model, usage.PromptTokens, usage.CompletionTokens)
	stats.CacheHit = false

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
	)

	session.AddMessage("user", message, emotionStr, nil)
	if reply != "" {
		session.AddMessage("assistant", reply, emotionStr, nil)
	}

	r.optimizer.SetCache(cacheKey, reply)

	r.logger.Info("[HandleChat] Complete", "reply_length", len(reply), "model", stats.Model, "tokens", stats.TotalTokens, "cost", stats.Cost)

	langfuseTrace.End()

	return reply, emotionStr, stats, nil
}

// HandleChatStream 处理聊天（流式）
func (r *Runtime) HandleChatStream(ctx context.Context, session *Session, message string) (<-chan string, <-chan *LLMStats, error) {
	startTime := time.Now()

	r.logger.Info("[HandleChatStream] Start", "sessionId", session.ID, "message", message)

	ctx, span := observability.StartSpanWithStartTime(ctx, "Agent.HandleChatStream",
		trace.WithAttributes(
			attribute.String("session_id", session.ID),
			attribute.String("player_id", session.PlayerID),
			attribute.String("tenant_id", session.TenantID),
		),
	)

	// Langfuse trace
	langfuseTrace := observability.StartLLMTrace("chat-stream", session.PlayerID, session.TenantID)

	// 1. 情绪检测子Span
	_, emotionSpan := observability.StartChildSpan(ctx, "Emotion.Detect")
	em := r.emotionDetector.Detect(message)
	emotionStr := string(em)
	observability.EndChildSpan(ctx, emotionSpan)

	// 2. 缓存查询子Span
	cacheKey := session.ID + "_" + message
	_, cacheSpan := observability.StartChildSpan(ctx, "Cache.Check")
	cached, hit := r.optimizer.GetCache(cacheKey)
	observability.EndChildSpan(ctx, cacheSpan)

	if hit && cached != "" {
		r.logger.Info("[HandleChatStream] Cache hit", "sessionId", session.ID, "cached_length", len(cached))
		span.SetAttributes(attribute.Bool("cache_hit", true))
		observability.AddEvent(ctx, "cache_hit", attribute.String("type", "exact"))
		observability.EndSpanWithDuration(ctx, span)
		langfuseTrace.End()
		observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()

		contentChan := make(chan string, 1)
		contentChan <- cached
		close(contentChan)

		statsChan := make(chan *LLMStats, 1)
		statsChan <- &LLMStats{CacheHit: true}
		close(statsChan)

		return contentChan, statsChan, nil
	}

	// 3. 构建上下文消息子Span
	_, contextSpan := observability.StartChildSpan(ctx, "Context.Build")
	messages := r.buildContextMessages(session, message)
	observability.EndChildSpan(ctx, contextSpan)

	// 4. LLM 健康检查子Span
	_, healthSpan := observability.StartChildSpan(ctx, "LLM.HealthCheck")
	isHealthy := r.llmAdapter.IsHealthy()
	observability.EndChildSpan(ctx, healthSpan)

	var stream llm.Stream
	var err error

	// 创建 LLM.StreamChat span（必须在 StreamChat 调用之前创建）
	llmCtx, llmSpan := observability.StartChildSpan(ctx, "LLM.StreamChat")

	if isHealthy {
		stream, err = r.llmAdapter.StreamChat(llmCtx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))

		if err != nil {
			r.logger.Error("[HandleChatStream] Primary LLM stream failed", "error", err)
			r.logger.Info("[HandleChatStream] Trying fallback adapter stream")
			stream, err = r.fallbackAdapter.StreamChat(llmCtx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		}
	} else {
		r.logger.Warn("[HandleChatStream] Primary LLM unhealthy, using fallback")
		stream, err = r.fallbackAdapter.StreamChat(llmCtx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
	}

	if err != nil {
		r.logger.Error("[HandleChatStream] All LLM stream calls failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
		observability.RecordError(span, err)
		observability.EndSpanWithDuration(ctx, span)
		observability.EndChildSpan(llmCtx, llmSpan)
		langfuseTrace.End()
		return nil, nil, fmt.Errorf("failed to get stream response: %w", err)
	}

	contentChan := make(chan string, 100)
	statsChan := make(chan *LLMStats, 1)

	go func() {
		defer close(contentChan)
		defer stream.Close()
		defer observability.EndChildSpan(llmCtx, llmSpan)

		var fullReply strings.Builder
		stats := &LLMStats{
			CacheHit: false,
		}

		chunkCount := 0
		var finishReason string
		var ttftMs int64 = 0
		var streamSpan trace.Span
		for {
			chunk, err := stream.Recv()

			// 流正常结束，退出循环
			if errors.Is(err, io.EOF) {
				break
			}

			// 流读取错误，记录错误并退出循环
			if err != nil {
				r.logger.Error("[HandleChatStream] Stream recv error", "error", err)
				_, errorSpan := observability.StartChildSpan(llmCtx, "LLM.StreamError")
				errorSpan.RecordError(err)
				errorSpan.End()
				break
			}

			// 空 chunk，跳过继续读取
			if chunk == nil {
				continue
			}

			// 收到第一个 chunk，结束等待 span 并记录耗时
			if chunkCount == 0 {
				// 创建 token 流传输 span
				_, streamSpan = observability.StartChildSpan(llmCtx, "LLM.TokenStreaming")
			}
			chunkCount++

			// chunk 有内容，发送给前端并累加到完整回复
			if chunk.Content != "" {
				contentChan <- chunk.Content
				fullReply.WriteString(chunk.Content)
			}

			// 记录模型名称（只记录一次）
			if chunk.Model != "" && stats.Model == "" {
				stats.Model = chunk.Model
			}

			// 记录 token 使用情况（usage 信息可能在最后一个 chunk 返回）
			if chunk.Usage.TotalTokens > 0 {
				stats.InputTokens = chunk.Usage.PromptTokens
				stats.OutputTokens = chunk.Usage.CompletionTokens
				stats.TotalTokens = chunk.Usage.TotalTokens
			}

			// 收到结束原因，退出循环（流式结束）
			if chunk.FinishReason != "" {
				finishReason = chunk.FinishReason
				break
			}
		}

		// 结束 token 流传输 span
		observability.EndChildSpan(llmCtx, streamSpan)

		// 创建统计指标记录 span
		_, postSpan := observability.StartChildSpan(llmCtx, "LLM.StatsAndMetrics")

		if fullReply.Len() == 0 && !strings.Contains(finishReason, "tool_calls") {
			r.logger.Warn("[HandleChatStream] Stream returned empty data, falling back to non-streaming")
			_, fallbackSpan := observability.StartChildSpan(llmCtx, "LLM.FallbackNonStream")
			r.handleNonStreamChat(ctx, messages, contentChan, &fullReply, stats)
			fallbackSpan.End()
		}

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

		// 在 LLM.StreamChat span 上添加关键属性
		llmSpan.SetAttributes(
			attribute.String("model", model),
			attribute.Int("input_tokens", stats.InputTokens),
			attribute.Int("output_tokens", stats.OutputTokens),
			attribute.Int("total_tokens", stats.TotalTokens),
			attribute.Float64("cost", stats.Cost),
			attribute.Int64("ttft_ms", ttftMs),
			attribute.Int("chunk_count", chunkCount),
			attribute.String("finish_reason", finishReason),
		)

		if model != "unknown" {
			span.SetAttributes(
				attribute.String("llm.model", model),
				attribute.Int("llm.input_tokens", stats.InputTokens),
				attribute.Int("llm.output_tokens", stats.OutputTokens),
				attribute.Int("llm.total_tokens", stats.TotalTokens),
				attribute.Float64("llm.cost", stats.Cost),
			)
		}

		postSpan.End()

		// 记录 session 更新 span
		_, sessionSpan := observability.StartChildSpan(llmCtx, "LLM.SessionUpdate")
		session.AddMessage("user", message, emotionStr, nil)
		if fullReply.Len() > 0 {
			session.AddMessage("assistant", fullReply.String(), emotionStr, nil)
		}
		sessionSpan.SetAttributes(
			attribute.Int("user_msg_len", len(message)),
			attribute.Int("assistant_msg_len", fullReply.Len()))
		sessionSpan.End()

		// 记录缓存写入 span
		if fullReply.Len() > 0 {
			_, cacheSpan := observability.StartChildSpan(llmCtx, "LLM.CacheWrite")
			r.optimizer.SetCache(cacheKey, fullReply.String())
			cacheSpan.SetAttributes(
				attribute.String("key", cacheKey),
				attribute.Int("value_len", fullReply.Len()))
			cacheSpan.End()
		}

		observability.EndChildSpan(llmCtx, llmSpan)

		observability.EndSpanWithDuration(ctx, span)

		langfuseTrace.End()

		statsChan <- stats
		close(statsChan)

		r.logger.Info("[HandleChatStream] Complete", "reply_length", fullReply.Len(), "latency", stats.LatencyMs)
	}()

	return contentChan, statsChan, nil
}

// handleNonStreamChat 处理非流式调用，并模拟流式响应
func (r *Runtime) handleNonStreamChat(ctx context.Context, messages []*eino_schema.Message, contentChan chan string, fullReply *strings.Builder, stats *LLMStats) {
	var msg *eino_schema.Message
	var usage *llm.ChatUsage
	var err error

	if r.llmAdapter.IsHealthy() {
		r.logger.Info("[HandleChatStream] Calling primary LLM (non-streaming)")
		msg, usage, err = r.llmAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		if err != nil {
			r.logger.Error("[HandleChatStream] Primary LLM failed", "error", err)
			r.logger.Info("[HandleChatStream] Trying fallback adapter (non-streaming)")
			msg, usage, err = r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
		}
	} else {
		r.logger.Info("[HandleChatStream] Using fallback adapter (non-streaming)")
		msg, usage, err = r.fallbackAdapter.Chat(ctx, messages, llm.WithTemperature(0.7), llm.WithMaxTokens(300))
	}

	if err != nil {
		r.logger.Error("[HandleChatStream] Non-streaming chat failed", "error", err)
		return
	}

	if msg == nil {
		return
	}

	content := msg.Content

	if usage != nil {
		stats.Model = usage.Model
		stats.InputTokens = usage.PromptTokens
		stats.OutputTokens = usage.CompletionTokens
		stats.TotalTokens = usage.TotalTokens
	}

	r.logger.Info("[HandleChatStream] Non-streaming response received", "content_len", len(content), "model", stats.Model, "inputTokens", stats.InputTokens, "outputTokens", stats.OutputTokens)

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
func (r *Runtime) buildContextMessages(session *Session, currentMessage string) []*eino_schema.Message {
	allMessages := session.GetMessages(0)

	if len(allMessages) == 0 {
		return []*eino_schema.Message{
			{Role: eino_schema.System, Content: SystemPrompt},
			{Role: eino_schema.User, Content: currentMessage},
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
			Content: currentMessage,
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
			r.logger.Info("[buildContextMessages] Context compressed",
				"old_count", len(oldMessages),
				"summary_len", len(newSummary),
				"recent_count", len(recentMessages))
		} else {
			r.logger.Warn("[buildContextMessages] Summarization failed, falling back to truncation", "error", err)
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
		Content: currentMessage,
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
