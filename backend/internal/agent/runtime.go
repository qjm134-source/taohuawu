package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/watertown/guide/internal/agent/tools"
	"github.com/watertown/guide/internal/cost"
	"github.com/watertown/guide/internal/emotion"
	"github.com/watertown/guide/internal/llm"
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
}

// Config Agent 配置
type Config struct {
	MaxRetries  int
	Timeout     time.Duration
	LLMTimeout  time.Duration
	ToolTimeout time.Duration
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
func (r *Runtime) HandleWelcome(ctx context.Context, session *Session) (string, error) {
	// 总超时
	ctx, cancel := utils.WithTimeoutFrom(ctx, r.config.Timeout)
	defer cancel()

	r.logger.Info("[HandleWelcome] Starting", "playerId", session.PlayerID)

	// 检查缓存
	if cached, hit := r.optimizer.GetCache("welcome_" + session.PlayerID); hit {
		r.logger.Info("[HandleWelcome] Cache hit", "playerId", session.PlayerID)
		return cached, nil
	}

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
		MaxTokens:   200,
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
		return "", fmt.Errorf("failed to get response: %w", err)
	}

	// 提取回复
	reply := response.Choices[0].Message.Content
	session.AddMessage("assistant", reply, "neutral", nil)

	// 缓存回复
	r.optimizer.SetCache("welcome_"+session.PlayerID, reply)

	return reply, nil
}

// HandleChat 处理聊天
func (r *Runtime) HandleChat(ctx context.Context, session *Session, message string) (string, string, *LLMStats, error) {
	r.logger.Info("[HandleChat] Start", "sessionId", session.ID, "message", message)

	ctx, cancel := utils.WithTimeoutFrom(ctx, r.config.Timeout)
	defer cancel()

	// 初始化统计信息
	stats := &LLMStats{}

	// 检测情绪
	em := r.emotionDetector.Detect(message)
	emotionStr := string(em)
	r.logger.Info("[HandleChat] Emotion detected", "emotion", emotionStr)

	// 检查缓存（使用 session ID 隔离，确保每个会话有独立缓存）
	cacheKey := session.ID + "_" + message
	if cached, hit := r.optimizer.GetCache(cacheKey); hit {
		r.logger.Info("[HandleChat] Cache hit", "sessionId", session.ID, "cached_length", len(cached))
		session.AddMessage("assistant", cached, emotionStr, nil)
		stats.CacheHit = true
		return cached, emotionStr, stats, nil
	}

	// 获取历史消息
	history := session.GetMessages(10)
	messages := []llm.Message{{Role: "system", Content: SystemPrompt}}

	// 添加历史
	for _, msg := range history {
		messages = append(messages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// 添加当前消息
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: message,
	})

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
		return "", "", nil, fmt.Errorf("failed to get response: %w", err)
	}

	// 如果 LLM 要求调用工具，执行工具并再次请求
	if response.HasToolCalls() {
		r.logger.Info("[HandleChat] LLM requested tool calls", "count", len(response.GetToolCalls()))
		response, err = r.handleToolCalls(ctx, response, messages, tools, stats)
		if err != nil {
			r.logger.Error("[HandleChat] Tool call handling failed", "error", err)
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

	// 添加到会话历史
	session.AddMessage("user", message, emotionStr, nil)
	session.AddMessage("assistant", reply, emotionStr, nil)

	// 缓存回复（使用 session ID 隔离）
	r.optimizer.SetCache(cacheKey, reply)

	r.logger.Info("[HandleChat] Complete", "reply_length", len(reply), "model", stats.Model, "tokens", stats.TotalTokens, "cost", stats.Cost)

	return reply, emotionStr, stats, nil
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
		result, err := r.toolRegistry.Execute(ctx, tc.Function.Name, args)
		if err != nil {
			r.logger.Error("[HandleChat] Tool execution failed", "name", tc.Function.Name, "error", err)
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
