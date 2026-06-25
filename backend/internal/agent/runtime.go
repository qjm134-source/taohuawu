package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

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

	// Langfuse trace
	langfuseTrace := observability.StartLLMTrace("welcome", session.PlayerID, session.TenantID)

	// 构建消息
	prompt := BuildWelcomePrompt(session.Nickname)

	messages := []llm.Message{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: prompt},
	}

	req := &llm.LLMRequest{
		Messages:    messages,
		Model:       "", // 留空，让路由根据策略自动选择模型
		Temperature: 0.7,
		MaxTokens:   500,
	}

	r.logger.Info("[HandleWelcome] LLM health check", "healthy", r.llmAdapter.IsHealthy())

	var response *llm.LLMResponse
	var err error

	// 尝试主 LLM（RouterAdapter 内部有降级链，会自动切换失败的 provider）
	// 使用独立超时，确保给 fallback 留出时间
	if r.llmAdapter.IsHealthy() {
		r.logger.Info("[HandleWelcome] Calling primary LLM", "model", req.Model)
		llmCtx, llmCancel := utils.WithTimeoutFrom(ctx, r.config.LLMTimeout)
		response, err = r.llmAdapter.Chat(llmCtx, req)
		llmCancel()

		if err != nil {
			r.logger.Error("[HandleWelcome] Primary LLM failed", "error", err)
			// 降级到兜底适配器，使用原始 ctx（不是已过期的 llmCtx）
			r.logger.Info("[HandleWelcome] Trying fallback adapter")
			response, err = r.fallbackAdapter.Chat(ctx, req)
		}
	} else {
		r.logger.Info("[HandleWelcome] Primary LLM unhealthy, using fallback")
		response, err = r.fallbackAdapter.Chat(ctx, req)
	}

	if err != nil {
		r.logger.Error("[HandleWelcome] All LLM failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("welcome", "error").Inc()
		langfuseTrace.RecordGeneration("llm-call", "unknown", req.Messages, "", 0, 0, 0, 0, err)
		langfuseTrace.End()

		// 检查是否启用兜底响应
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

	// 提取回复
	reply := response.Choices[0].Message.Content
	session.AddMessage("assistant", reply, "neutral", nil)

	// 记录 LLM 指标
	model := response.Model
	if model == "" {
		model = "unknown"
	}
	llmCost := cost.CalculateCost(model, response.Usage.PromptTokens, response.Usage.CompletionTokens)
	latencyMs := time.Since(startTime).Milliseconds()

	observability.LLMRequestsTotal.WithLabelValues(model, "success").Inc()
	observability.LLMRequestDuration.WithLabelValues(model).Observe(time.Since(startTime).Seconds())
	observability.LLMRequestTokens.WithLabelValues(model).Add(float64(response.Usage.PromptTokens))
	observability.LLMCompletionTokens.WithLabelValues(model).Add(float64(response.Usage.CompletionTokens))
	observability.CostTotal.WithLabelValues(model).Add(llmCost)
	observability.AgentRequestsTotal.WithLabelValues("welcome", "success").Inc()
	observability.AgentRequestDuration.WithLabelValues("welcome").Observe(time.Since(startTime).Seconds())

	// 记录 Langfuse generation
	langfuseTrace.RecordGeneration("llm-call", model, req.Messages, reply,
		response.Usage.PromptTokens, response.Usage.CompletionTokens, llmCost, latencyMs, nil)
	langfuseTrace.End()

	// 缓存回复
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
	r.logger.Info("[HandleChat] Emotion detected", "emotion", emotionStr)

	// 检查缓存（使用 session ID 隔离，确保每个会话有独立缓存）
	cacheKey := session.ID + "_" + message
	if cached, hit := r.optimizer.GetCache(cacheKey); hit {
		r.logger.Info("[HandleChat] Exact cache hit", "sessionId", session.ID, "cached_length", len(cached))
		span.SetAttributes(attribute.Bool("cache_hit", true))
		session.AddMessage("assistant", cached, emotionStr, nil)
		stats.CacheHit = true
		observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
		langfuseTrace.End()
		return cached, emotionStr, stats, nil
	}

	// 检查语义缓存（相似问题匹配）
	if cached, hit := r.optimizer.CheckSimilarity(ctx, message, 0.85); hit {
		r.logger.Info("[HandleChat] Semantic cache hit", "sessionId", session.ID, "cached_length", len(cached))
		span.SetAttributes(attribute.Bool("cache_hit", true))
		session.AddMessage("assistant", cached, emotionStr, nil)
		stats.CacheHit = true
		observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
		langfuseTrace.End()
		return cached, emotionStr, stats, nil
	}

	// 构建上下文消息（含摘要压缩）
	messages := r.buildContextMessages(session, message)

	// 把工具注册表中的工具注册给 LLM
	tools := tools.ConvertAllTools(r.toolRegistry)

	r.logger.Info("[HandleChat] Building LLM request", "message_count", len(messages), "tools_count", len(tools))

	req := &llm.LLMRequest{
		Messages:    messages,
		Model:       "", // 留空，让路由根据策略自动选择模型
		Temperature: 0.7,
		MaxTokens:   300,
		Tools:       tools,
	}

	var response *llm.LLMResponse
	var err error
	startTime := time.Now()

	// 尝试主 LLM，使用独立超时确保给 fallback 留出时间
	r.logger.Info("[HandleChat] Checking LLM health", "healthy", r.llmAdapter.IsHealthy())
	if r.llmAdapter.IsHealthy() {
		r.logger.Info("[HandleChat] Calling primary LLM")
		llmCtx, llmCancel := utils.WithTimeoutFrom(ctx, r.config.LLMTimeout)
		response, err = r.llmAdapter.Chat(llmCtx, req)
		llmCancel()

		if err != nil {
			r.logger.Error("[HandleChat] Primary LLM failed", "error", err)
			// 降级到备用适配器，使用原始 ctx（不是已过期的 llmCtx）
			r.logger.Info("[HandleChat] Trying fallback adapter")
			response, err = r.fallbackAdapter.Chat(ctx, req)
		}
	} else {
		// 使用兜底回复
		r.logger.Warn("[HandleChat] Primary LLM unhealthy, using fallback")
		response, err = r.fallbackAdapter.Chat(ctx, req)
	}

	if err != nil {
		r.logger.Error("[HandleChat] All LLM calls failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
		observability.RecordError(span, err)
		langfuseTrace.RecordGeneration("llm-call", "unknown", req.Messages, "", 0, 0, 0, 0, err)
		langfuseTrace.End()
		return "", "", nil, fmt.Errorf("failed to get response: %w", err)
	}

	// 如果 LLM 要求调用工具，执行工具并再次请求
	if response.HasToolCalls() {
		r.logger.Info("[HandleChat] LLM requested tool calls", "count", len(response.GetToolCalls()))
		response, err = r.handleToolCalls(ctx, response, messages, tools, stats)
		if err != nil {
			r.logger.Error("[HandleChat] Tool call handling failed", "error", err)
			observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
			langfuseTrace.RecordGeneration("llm-call", "unknown", req.Messages, "", 0, 0, 0, 0, err)
			langfuseTrace.End()
			return "", "", nil, fmt.Errorf("failed to handle tool calls: %w", err)
		}
	}

	r.logger.Info("[HandleChat] LLM response received", "choices_count", len(response.Choices))

	// 提取回复
	reply := strings.TrimSpace(response.Choices[0].Message.Content)

	// 填充统计信息
	stats.Model = response.Model
	stats.LatencyMs = time.Since(startTime).Milliseconds()
	stats.InputTokens = response.Usage.PromptTokens
	stats.OutputTokens = response.Usage.CompletionTokens
	stats.TotalTokens = response.Usage.TotalTokens
	stats.Cost = cost.CalculateCost(response.Model, response.Usage.PromptTokens, response.Usage.CompletionTokens)
	stats.CacheHit = false

	// 记录 LLM 指标
	r.recordLLMMetrics(response.Model, "success", time.Since(startTime).Seconds(),
		response.Usage.PromptTokens, response.Usage.CompletionTokens, stats.Cost)
	observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
	observability.AgentRequestDuration.WithLabelValues("chat").Observe(time.Since(startTime).Seconds())

	span.SetAttributes(
		attribute.String("llm.model", response.Model),
		attribute.Int("llm.input_tokens", response.Usage.PromptTokens),
		attribute.Int("llm.output_tokens", response.Usage.CompletionTokens),
		attribute.Int("llm.total_tokens", response.Usage.TotalTokens),
		attribute.Float64("llm.cost", stats.Cost),
	)

	// 添加到会话历史（仅当回复非空时才添加 assistant 消息，避免后续请求携带空内容导致 API 校验失败）
	session.AddMessage("user", message, emotionStr, nil)
	if reply != "" {
		session.AddMessage("assistant", reply, emotionStr, nil)
	}

	// 缓存回复（使用 session ID 隔离）
	r.optimizer.SetCache(cacheKey, reply)

	r.logger.Info("[HandleChat] Complete", "reply_length", len(reply), "model", stats.Model, "tokens", stats.TotalTokens, "cost", stats.Cost)

	// 记录 Langfuse generation
	langfuseTrace.RecordGeneration("llm-call", stats.Model, req.Messages, reply,
		stats.InputTokens, stats.OutputTokens, stats.Cost, stats.LatencyMs, nil)
	langfuseTrace.End()

	return reply, emotionStr, stats, nil
}

// HandleChatStream 处理聊天（流式）
func (r *Runtime) HandleChatStream(ctx context.Context, session *Session, message string) (<-chan string, <-chan *LLMStats, error) {
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
		observability.EndSpanWithDuration(ctx, span)
		langfuseTrace.End()
		observability.AgentRequestsTotal.WithLabelValues("chat", "success").Inc()
		contentChan := make(chan string, 1)
		statsChan := make(chan *LLMStats, 1)
		contentChan <- cached
		close(contentChan)
		statsChan <- &LLMStats{CacheHit: true}
		close(statsChan)
		return contentChan, statsChan, nil
	}

	// 3. 构建上下文消息子Span
	_, contextSpan := observability.StartChildSpan(ctx, "Context.Build")
	messages := r.buildContextMessages(session, message)
	observability.EndChildSpan(ctx, contextSpan)

	// 4. 工具注册子Span
	_, toolsSpan := observability.StartChildSpan(ctx, "Tools.Register")
	tools := tools.ConvertAllTools(r.toolRegistry)
	observability.EndChildSpan(ctx, toolsSpan)

	r.logger.Info("[HandleChatStream] Building LLM request", "message_count", len(messages))

	req := &llm.LLMRequest{
		Messages:    messages,
		Model:       "", // 留空，让路由根据策略自动选择模型
		Temperature: 0.7,
		MaxTokens:   300,
		Tools:       tools,
	}

	startTime := time.Now()

	// 尝试流式调用主 LLM
	var stream <-chan llm.StreamChunk
	var err error

	// 5. LLM健康检查子Span
	_, healthSpan := observability.StartChildSpan(ctx, "LLM.HealthCheck")
	isHealthy := r.llmAdapter.IsHealthy()
	observability.EndChildSpan(ctx, healthSpan)

	r.logger.Info("[HandleChatStream] Checking LLM health", "healthy", isHealthy)

	// 6. LLM调用子Span（覆盖完整的流式调用生命周期）
	llmCtx, llmSpan := observability.StartChildSpan(ctx, "LLM.StreamChat")
	if isHealthy {
		r.logger.Info("[HandleChatStream] Calling primary LLM stream")
		stream, err = r.llmAdapter.StreamChat(ctx, req)

		if err != nil {
			r.logger.Error("[HandleChatStream] Primary LLM stream failed", "error", err)
			r.logger.Info("[HandleChatStream] Trying fallback adapter stream")
			stream, err = r.fallbackAdapter.StreamChat(ctx, req)
		}
	} else {
		r.logger.Warn("[HandleChatStream] Primary LLM unhealthy, using fallback")
		stream, err = r.fallbackAdapter.StreamChat(ctx, req)
	}

	// 记录 StreamChat 返回的时间点
	streamReadyTime := time.Now()

	if err != nil {
		observability.EndChildSpan(llmCtx, llmSpan)
		r.logger.Error("[HandleChatStream] All LLM stream calls failed", "error", err)
		observability.AgentRequestsTotal.WithLabelValues("chat", "error").Inc()
		observability.RecordError(span, err)
		observability.EndSpanWithDuration(ctx, span)
		langfuseTrace.RecordGeneration("llm-call", "unknown", req.Messages, "", 0, 0, 0, 0, err)
		langfuseTrace.End()
		return nil, nil, fmt.Errorf("failed to get stream response: %w", err)
	}

	contentChan := make(chan string, 100)
	statsChan := make(chan *LLMStats, 1)

	go func() {
		defer close(contentChan)

		var fullReply strings.Builder
		stats := &LLMStats{
			CacheHit: false,
		}

		// 7a. 等待首Token子Span：覆盖从 StreamChat 返回到收到第一个 chunk 之间的时间（TTFT）
		// 使用 streamReadyTime 作为开始时间，确保 span 从 StreamChat 返回时就开始计时
		_, ttftSpan := observability.StartChildSpanAt(llmCtx, "LLM.WaitForFirstToken", streamReadyTime)

		chunkCount := 0
		for chunk := range stream {
			// 收到第一个 chunk 时结束 TTFT span
			if chunkCount == 0 {
				observability.EndChildSpan(llmCtx, ttftSpan)
			}
			chunkCount++
			r.logger.Info("[HandleChatStream] Received chunk", "index", chunkCount, "content_len", len(chunk.Content), "model", chunk.Model, "finish_reason", chunk.FinishReason)

			if chunk.Content != "" {
				contentChan <- chunk.Content
				fullReply.WriteString(chunk.Content)
			}

			if chunk.Model != "" && stats.Model == "" {
				stats.Model = chunk.Model
				r.logger.Info("[HandleChatStream] Model detected", "model", stats.Model)
			}

			if chunk.Usage.TotalTokens > 0 {
				stats.InputTokens = chunk.Usage.PromptTokens
				stats.OutputTokens = chunk.Usage.CompletionTokens
				stats.TotalTokens = chunk.Usage.TotalTokens
			}

			if chunk.FinishReason != "" {
				break
			}
		}
		// 结束 LLM.StreamChat Span（包含请求发送、等待首token、接收数据）
		observability.EndChildSpan(llmCtx, llmSpan)

		if fullReply.Len() == 0 {
			r.logger.Warn("[HandleChatStream] Stream returned empty data, falling back to non-streaming")
			r.handleNonStreamChat(ctx, req, contentChan, &fullReply, stats)
		}

		if stats.Model == "" {
			r.logger.Warn("[HandleChatStream] No model detected in stream")
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

		if model != "unknown" {
			span.SetAttributes(
				attribute.String("llm.model", model),
				attribute.Int("llm.input_tokens", stats.InputTokens),
				attribute.Int("llm.output_tokens", stats.OutputTokens),
				attribute.Int("llm.total_tokens", stats.TotalTokens),
				attribute.Float64("llm.cost", stats.Cost),
			)
		}

		// 8. 会话更新子Span
		_, sessionSpan := observability.StartChildSpan(ctx, "Session.Update")
		session.AddMessage("user", message, emotionStr, nil)
		if fullReply.Len() > 0 {
			session.AddMessage("assistant", fullReply.String(), emotionStr, nil)
		}
		observability.EndChildSpan(ctx, sessionSpan)

		// 9. 缓存写入子Span
		_, cacheWriteSpan := observability.StartChildSpan(ctx, "Cache.Write")
		if fullReply.Len() > 0 {
			r.optimizer.SetCache(cacheKey, fullReply.String())
		}
		observability.EndChildSpan(ctx, cacheWriteSpan)

		observability.EndSpanWithDuration(ctx, span)

		langfuseTrace.RecordGeneration("llm-call", stats.Model, req.Messages, fullReply.String(),
			stats.InputTokens, stats.OutputTokens, stats.Cost, stats.LatencyMs, nil)
		langfuseTrace.End()

		statsChan <- stats
		close(statsChan)

		r.logger.Info("[HandleChatStream] Complete", "reply_length", fullReply.Len(), "latency", stats.LatencyMs)
	}()

	return contentChan, statsChan, nil
}

// handleToolCalls 执行 LLM 请求的工具调用，并将结果再次提交给 LLM 生成最终回复。
func (r *Runtime) handleToolCalls(
	ctx context.Context,
	firstResponse *llm.LLMResponse,
	messages []llm.Message,
	tools []llm.LLMTool,
	stats *LLMStats,
) (*llm.LLMResponse, error) {
	// 把 assistant 的工具调用请求加入对话历史
	assistantMsg := llm.Message{
		Role:      "assistant",
		Content:   firstResponse.Choices[0].Message.Content,
		ToolCalls: firstResponse.GetToolCalls(),
	}
	messages = append(messages, assistantMsg)

	// 执行每个工具调用
	for _, tc := range firstResponse.GetToolCalls() {
		if tc.Type != "function" && tc.Type != "" {
			r.logger.Warn("[HandleChat] Unsupported tool call type", "type", tc.Type)
			continue
		}

		var args map[string]interface{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				r.logger.Error("[HandleChat] Failed to parse tool arguments", "error", err, "arguments", tc.Function.Arguments)
				args = map[string]interface{}{}
			}
		}

		// 记录本次调用了哪些工具
		stats.ToolsUsed = append(stats.ToolsUsed, tc.Function.Name)

		r.logger.Info("[HandleChat] Executing tool", "name", tc.Function.Name, "args", args)
		result, err := r.executeToolWithRetry(ctx, tc.Function.Name, args)
		if err != nil {
			r.logger.Error("[HandleChat] Tool execution failed after retries", "name", tc.Function.Name, "error", err)
			result = map[string]interface{}{"error": err.Error()}
		}

		resultJSON, _ := json.Marshal(result)
		messages = append(messages, llm.Message{
			Role:       "tool",
			Content:    string(resultJSON),
			ToolCallID: tc.ID,
		})
	}

	// 再次请求 LLM，让模型基于工具结果生成回复
	req := &llm.LLMRequest{
		Messages:    messages,
		Model:       "",
		Temperature: 0.7,
		MaxTokens:   300,
		Tools:       tools,
	}

	r.logger.Info("[HandleChat] Calling LLM after tool execution")
	llmCtx, llmCancel := utils.WithTimeoutFrom(ctx, r.config.LLMTimeout)
	defer llmCancel()

	response, err := r.llmAdapter.Chat(llmCtx, req)
	if err != nil {
		r.logger.Error("[HandleChat] LLM after tool calls failed", "error", err)
		return nil, err
	}

	return response, nil
}

// executeToolWithRetry 执行工具调用，支持自动重试。
func (r *Runtime) executeToolWithRetry(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	maxRetries := r.config.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err := r.toolRegistry.Execute(ctx, toolName, args)
		if err == nil {
			if attempt > 1 {
				r.logger.Info("[executeToolWithRetry] Tool succeeded after retry",
					"name", toolName, "attempt", attempt)
			}
			return result, nil
		}

		lastErr = err
		r.logger.Warn("[executeToolWithRetry] Tool execution failed, retrying",
			"name", toolName, "attempt", attempt, "maxRetries", maxRetries, "error", err)

		// 最后一次失败不等待
		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
	}

	return nil, fmt.Errorf("tool %s failed after %d retries: %w", toolName, maxRetries, lastErr)
}

// handleNonStreamChat 处理非流式调用，并模拟流式响应
func (r *Runtime) handleNonStreamChat(ctx context.Context, req *llm.LLMRequest, contentChan chan string, fullReply *strings.Builder, stats *LLMStats) {
	var resp *llm.LLMResponse
	var err error

	if r.llmAdapter.IsHealthy() {
		r.logger.Info("[HandleChatStream] Calling primary LLM (non-streaming)")
		resp, err = r.llmAdapter.Chat(ctx, req)
		if err != nil {
			r.logger.Error("[HandleChatStream] Primary LLM failed", "error", err)
			r.logger.Info("[HandleChatStream] Trying fallback adapter (non-streaming)")
			resp, err = r.fallbackAdapter.Chat(ctx, req)
		}
	} else {
		r.logger.Info("[HandleChatStream] Using fallback adapter (non-streaming)")
		resp, err = r.fallbackAdapter.Chat(ctx, req)
	}

	if err != nil {
		r.logger.Error("[HandleChatStream] Non-streaming chat failed", "error", err)
		return
	}

	// 如果 LLM 返回了工具调用，执行工具并再次请求 LLM 获取最终回复
	if resp.HasToolCalls() {
		r.logger.Info("[HandleChatStream] Non-streaming response has tool calls", "count", len(resp.GetToolCalls()))
		resp2, toolErr := r.handleToolCalls(ctx, resp, req.Messages, req.Tools, stats)
		if toolErr != nil {
			r.logger.Error("[HandleChatStream] Tool call handling in non-streaming fallback failed", "error", toolErr)
			return
		}
		resp = resp2
	}

	// 获取回复内容
	content := ""
	if len(resp.Choices) > 0 && len(resp.Choices[0].Message.Content) > 0 {
		content = resp.Choices[0].Message.Content
	}

	// 始终填充统计信息（即使内容为空）
	if resp.Model != "" {
		stats.Model = resp.Model
	}
	stats.InputTokens = resp.Usage.PromptTokens
	stats.OutputTokens = resp.Usage.CompletionTokens
	stats.TotalTokens = resp.Usage.TotalTokens

	r.logger.Info("[HandleChatStream] Non-streaming response received", "content_len", len(content), "model", resp.Model, "inputTokens", resp.Usage.PromptTokens, "outputTokens", resp.Usage.CompletionTokens)

	// 模拟流式响应：逐字符发送
	if content != "" {
		// 逐字符发送以模拟流式效果
		for _, char := range content {
			contentChan <- string(char)
			fullReply.WriteRune(char)
		}
	} else {
		// 如果内容为空，发送一个空字符通知前端
		contentChan <- ""
	}
}

// buildContextMessages 构建 LLM 请求的消息上下文。
// 优先使用 LLM 摘要替代硬截断：当历史消息预估 Token 数超过阈值时，
// 将早期消息压缩为一段摘要文本，保留最近的 N 条原始消息。
func (r *Runtime) buildContextMessages(session *Session, currentMessage string) []llm.Message {
	allMessages := session.GetMessages(0) // 获取全部消息

	// 如果没有历史或是欢迎场景，直接构建
	if len(allMessages) == 0 {
		return []llm.Message{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: currentMessage},
		}
	}

	// 获取摘要器
	summarizer := cost.GetSummarizer()

	// 预估所有历史消息 + 当前消息的 Token 总数
	totalTokens := 0
	for _, msg := range allMessages {
		if summarizer != nil {
			totalTokens += summarizer.EstimateTokens(msg.Content)
		} else {
			// 无摘要器时用简单估算：1 中文 ≈ 2 tokens
			totalTokens += len([]rune(msg.Content)) * 2
		}
	}
	if summarizer != nil {
		totalTokens += summarizer.EstimateTokens(currentMessage)
	} else {
		totalTokens += len([]rune(currentMessage)) * 2
	}

	const tokenThreshold = 4096

	// 未超过阈值，构建完整消息列表
	if totalTokens <= tokenThreshold {
		messages := []llm.Message{{Role: "system", Content: SystemPrompt}}
		for _, msg := range allMessages {
			if msg.Content == "" {
				continue
			}
			messages = append(messages, llm.Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: currentMessage,
		})
		return messages
	}

	// 超过阈值，尝试使用 LLM 摘要压缩
	keepRecent := 6 // 保留最近 6 条消息作为原始上下文
	if len(allMessages) <= keepRecent {
		keepRecent = len(allMessages) / 2
		if keepRecent < 2 {
			keepRecent = 2
		}
	}

	recentMessages := allMessages[len(allMessages)-keepRecent:]
	oldMessages := allMessages[:len(allMessages)-keepRecent]

	messages := []llm.Message{{Role: "system", Content: SystemPrompt}}

	// 尝试获取已有摘要并增量更新
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
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "[对话历史摘要] " + newSummary,
			})
			r.logger.Info("[buildContextMessages] Context compressed",
				"old_count", len(oldMessages),
				"summary_len", len(newSummary),
				"recent_count", len(recentMessages))
		} else {
			r.logger.Warn("[buildContextMessages] Summarization failed, falling back to truncation", "error", err)
			// 降级：使用简单的截断提示
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: fmt.Sprintf("[之前有%d条对话，以下是最近的对话内容]", len(oldMessages)),
			})
		}
	} else if len(oldMessages) > 0 {
		// 无摘要器，使用简单截断提示
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: fmt.Sprintf("[之前有%d条对话，以下是最近的对话内容]", len(oldMessages)),
		})
	}

	// 添加最近的原始消息
	for _, msg := range recentMessages {
		if msg.Content == "" {
			continue
		}
		messages = append(messages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// 添加当前消息
	messages = append(messages, llm.Message{
		Role:    "user",
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
