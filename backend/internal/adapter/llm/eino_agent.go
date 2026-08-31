package llm

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	eino_openai "github.com/cloudwego/eino-ext/components/model/openai"
	eino_adk "github.com/cloudwego/eino/adk"
	eino_model "github.com/cloudwego/eino/components/model"
	eino_tool "github.com/cloudwego/eino/components/tool"
	eino_compose "github.com/cloudwego/eino/compose"
	eino_schema "github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/watertown/guide/internal/config"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/pkg/contextutil"
	"github.com/watertown/guide/pkg/logging"
)

type Strategy string

const (
	StrategyFixed      Strategy = "fixed"
	StrategyCost       Strategy = "cost"
	StrategyLatency    Strategy = "latency"
	StrategyCapability Strategy = "capability"
	StrategyFallback   Strategy = "fallback"
	StrategyWeighted   Strategy = "weighted"
)

const (
	streamResultBuffer = 100
	unknownModelName   = "unknown"
	maxADKIterations   = 20
)

var defaultHTTPClient = &http.Client{
	// 建TCP连接 → TLS握手 → 发送请求 → 等待响应头 → 读取响应体
	// └───── http.Client.Timeout 管这一整段 ───────┘
	// Timeout: 60 * time.Second, // Timeout 会在超时后取消正在进行的流式响应，流式场景应改用 context.WithTimeout 控制整体会话，或干脆不设超时由上层兜底
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second, // TCP 建连
			KeepAlive: 30 * time.Second, // TCP 层 keepalive 探测间隔
			DualStack: true,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second, // 等响应头最多 30 秒。首token。生产中应根据目标服务的 P99 首 token 延迟来设，通常给到 30~60 秒。设 0（不设）：没有任何保护，服务端假死时请求永久挂起，goroutine 和连接持续堆积——这是更常见的生产事故来源。
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	},
}

type modelEntry struct {
	name      string
	modelName string
	model     eino_model.ToolCallingChatModel
	weight    float64
}

type modelStats struct {
	totalLatency time.Duration
	requestCount int
	errorCount   int
	emaLatency   time.Duration // 延迟指数移动平均，零值表示尚无采样数据
}

// emaAlpha EMA 平滑系数：新样本所占权重，越大对新样本越敏感。
// 0.3 兼顾对延迟变化的响应速度与抖动平滑，是延迟路由的常用取值。
const emaAlpha = 0.3

// updateEMA 更新延迟指数移动平均；首个样本直接作为初值，避免从 0 爬升。
func (s *modelStats) updateEMA(latency time.Duration) {
	if s.emaLatency <= 0 {
		s.emaLatency = latency
		return
	}
	s.emaLatency = time.Duration(emaAlpha*float64(latency) + (1-emaAlpha)*float64(s.emaLatency))
}

type EinoAgentAdapter struct {
	mu               sync.RWMutex
	agent            *eino_adk.ChatModelAgent
	runner           *eino_adk.Runner
	models           []modelEntry
	fallback         []string
	strategy         Strategy
	fixedModel       string
	weights          map[string]float64
	logger           logging.Logger
	stats            map[string]*modelStats
	nameToKey        map[string]string // 原始模型名 → sanitize 后的 stats key（LLM 响应返回的是原始名）
	capabilityMap    map[string][]string
	timeout          time.Duration
	primaryModelName string
	primaryIndex     int
	maxRetries       int
	tools            []eino_tool.InvokableTool // 保存工具引用，EMA 切换主模型时需重建 agent
	circuits         *circuitManager           // 每模型熔断器：选型时跳过不可用模型，请求后记录成败
}

func NewEinoAgentAdapter(logger logging.Logger, cfg config.LLMConfig, circuitCfg config.CircuitConfig, tools []eino_tool.InvokableTool) *EinoAgentAdapter {
	adapter := &EinoAgentAdapter{
		logger:        logger,
		strategy:      parseStrategy(cfg.Strategy),
		weights:       make(map[string]float64),
		stats:         make(map[string]*modelStats),
		nameToKey:     make(map[string]string),
		capabilityMap: make(map[string][]string),
		timeout:       cfg.Timeout.Duration,
		maxRetries:    cfg.MaxRetries,
		tools:         tools,
		circuits:      newCircuitManager(circuitCfg, logger),
	}

	for _, mc := range cfg.Models {
		if !mc.Enabled {
			continue
		}

		chatModel, err := eino_openai.NewChatModel(context.Background(), &eino_openai.ChatModelConfig{
			Model:      mc.Name,
			APIKey:     mc.APIKey,
			BaseURL:    mc.BaseURL,
			HTTPClient: defaultHTTPClient,
		})
		if err != nil {
			logger.Error("Failed to create Eino model", "model", mc.Name, "error", err)
			continue
		}

		name := sanitizeProviderName(mc.Name)
		adapter.nameToKey[mc.Name] = name
		adapter.models = append(adapter.models, modelEntry{
			name:      name,
			modelName: mc.Name,
			model:     chatModel,
		})
		adapter.fallback = append(adapter.fallback, name)
		adapter.stats[name] = &modelStats{}

	}

	// 预注册全部模型 key：让熔断器感知尚未被调用过的备用模型，
	// 避免主模型熔断时 AllUnavailable 误判"全部不可用"而拒绝可降级的请求
	keys := make([]string, 0, len(adapter.models))
	for _, m := range adapter.models {
		keys = append(keys, m.name)
	}
	adapter.circuits.registerKeys(keys...)

	if len(adapter.models) > 0 {
		primaryModel := adapter.selectPrimaryModel()
		adapter.primaryModelName = primaryModel.modelName

		for i, m := range adapter.models {
			if m.name == primaryModel.name {
				adapter.primaryIndex = i
				break
			}
		}

		agent, err := adapter.buildADKAgent(tools)
		if err != nil {
			logger.Error("Failed to build ADK agent", "error", err)
			return adapter
		}
		adapter.agent = agent

		adapter.runner = adapter.buildRunner(agent)
	}

	return adapter
}

func (a *EinoAgentAdapter) buildADKAgent(tools []eino_tool.InvokableTool) (*eino_adk.ChatModelAgent, error) {
	primaryModel := a.models[a.primaryIndex]

	var toolsConfig eino_adk.ToolsConfig
	if len(tools) > 0 {
		baseTools := make([]eino_tool.BaseTool, 0, len(tools))
		for _, t := range tools {
			baseTools = append(baseTools, t)
		}
		toolsConfig = eino_adk.ToolsConfig{
			ToolsNodeConfig: eino_compose.ToolsNodeConfig{
				Tools: baseTools,
			},
		}
	}

	var failoverConfig *eino_adk.ModelFailoverConfig[*eino_schema.Message]
	if len(a.models) > 1 {
		failoverConfig = &eino_adk.ModelFailoverConfig[*eino_schema.Message]{
			MaxRetries: uint(a.maxRetries),
			// ShouldFailover：每轮模型调用结束后由 ADK 回调，判断是否需要进入下一次故障转移。
			// 返回 true = 继续尝试下一个模型；返回 false = 停止故障转移，把当前结果/错误返回给调用方。
			//
			// 判定顺序遵循「越明确的退出信号越先检查」：
			//  1) ctx 已取消/超时：用户侧主动中断，故障转移无意义，立即停止
			//     （注意：ADK 对 ctx.Err()!=nil 的情况实际会跳过本函数直接停止，
			//      这里再次判断是防止将来配置模型级重试时，RetryExhaustedError 包装 context 错误后被误判为需要转移）
			//  2) 有明确错误：模型返回 transport/auth/rpc 错误等，换模型有概率解决
			//  3) 输出为空：模型"成功"但内容为空（如 content policy 拒绝、provider 异常返回 200 空 body），
			//     对业务来说等同于失败，也应切换模型兜底
			ShouldFailover: func(ctx context.Context, output *eino_schema.Message, err error) bool {
				if ctx.Err() != nil {
					return false
				}
				if err != nil {
					return true
				}
				if output == nil || output.Content == "" {
					return true
				}
				return false
			},
			GetFailoverModel: func(ctx context.Context, failoverCtx *eino_adk.FailoverContext[*eino_schema.Message]) (eino_model.BaseModel[*eino_schema.Message], []*eino_schema.Message, error) {
				return a.getFailoverModel(ctx, failoverCtx)
			},
		}
	}

	config := &eino_adk.ChatModelAgentConfig{
		Name:                "WaterTownGuide",
		Instruction:         "你是桃花坞的智能导游小荷。请根据用户的问题，使用可用的工具获取信息，然后生成友好、详细的回答。在收到工具执行结果后，请总结结果并给出最终回复。",
		Model:               primaryModel.model,
		ToolsConfig:         toolsConfig,
		ModelFailoverConfig: failoverConfig,
		MaxIterations:       maxADKIterations,
	}

	agent, err := eino_adk.NewChatModelAgent(context.Background(), config)
	if err != nil {
		a.logger.Error("Failed to create ADK ChatModelAgent", "error", err)
		return nil, err
	}

	return agent, nil
}

// getFailoverModel 是 ADK ModelFailoverConfig 的回调：当主模型（或上一轮备用模型）调用失败时，
// 由 ADK 内部调用此函数选择下一个用于故障转移的模型。
//
// 轮次策略：按配置顺序从主模型向后兜底（主模型失败 → models[primaryIndex+1] → models[primaryIndex+2] …），
// 利用模运算实现环形排列，但最终用 `idx == primaryIndex` 防止绕一圈回到主模型本身。
// 已被熔断的候选（open/半开许可耗尽/hard 冷却中）直接跳过，不浪费故障转移次数；
// 全部候选被跳过时终止故障转移（返回 nil），由上层关键词兜底接管。
//
// 返回值语义（与 ADK 约定一致）：
//   - (nil, nil, nil)：没有可用的备用模型，ADK 将停止故障转移并把当前错误返回给调用方
//   - (model, msgs, nil)：使用指定模型和输入消息继续尝试；msgs 为 nil 时沿用原始输入
//   - (_, _, err)：故障转移本身出错，ADK 立即停止并把 err 直接返回给调用方
//
// 并发注意：调用方持有 ADK 内部的故障转移上下文，会串行调用本函数；
// a.primaryIndex 可能在运行时被 EMA 路由修改（写锁保护），但这里仅做读取无需加锁——
// 即使读到旧值，取模兜底的顺序仍然合法，最多本轮顺序略有偏差，不会引用越界。
func (a *EinoAgentAdapter) getFailoverModel(ctx context.Context, failoverCtx *eino_adk.FailoverContext[*eino_schema.Message]) (eino_model.BaseModel[*eino_schema.Message], []*eino_schema.Message, error) {
	attempt := int(failoverCtx.FailoverAttempt) // 从 1 开始计数，第 N 次故障转移
	n := len(a.models)

	// 没有可兜底的模型（仅 1 个）或已用尽全部兜底次数，终止故障转移
	if n <= 1 || attempt >= n {
		return nil, nil, nil
	}

	// 从主模型位置 + attempt 起扫描候选，跳过被熔断的模型；
	// 最多扫描 n-1 个（不含主模型本身）。用 Available（无副作用查询）过滤，
	// 避免扫描期间消耗半开探测 permit：探测机会应留给真正发起调用的那次请求。
	for i := 0; i < n-1; i++ {
		idx := (a.primaryIndex + attempt + i) % n
		if idx == a.primaryIndex {
			break // 绕回主模型（理论上不会触发），保险地终止
		}
		entry := a.models[idx]
		if !a.circuits.Available(entry.name) {
			continue
		}
		// 输入消息不做转换（不同 provider 间 schema 一致），直接把 ADK 传入的原始输入传下去
		return entry.model, failoverCtx.InputMessages, nil
	}

	// 全部备用模型均不可用，终止故障转移
	return nil, nil, nil
}

func (a *EinoAgentAdapter) buildRunner(agent *eino_adk.ChatModelAgent) *eino_adk.Runner {
	return eino_adk.NewRunner(context.Background(), eino_adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})
}

func (a *EinoAgentAdapter) selectPrimaryModel() modelEntry {
	switch a.strategy {
	case StrategyFixed:
		for _, m := range a.models {
			if m.name == a.fixedModel {
				return m
			}
		}
	case StrategyFallback:
		if len(a.fallback) > 0 {
			for _, m := range a.models {
				if m.name == a.fallback[0] {
					return m
				}
			}
		}
	case StrategyWeighted:
		r := rand.Float64()
		sum := 0.0
		for _, m := range a.models {
			sum += a.weights[m.name]
			if r <= sum {
				return m
			}
		}
	case StrategyCost, StrategyLatency, StrategyCapability:
	}
	if len(a.models) > 0 {
		return a.models[0]
	}
	return modelEntry{}
}

func (a *EinoAgentAdapter) IsHealthy() bool {
	return a.agent != nil && a.runner != nil
}

// emaSwitchMargin 主模型切换迟滞阈值：新模型 EMA 需比当前低此比例才切换，
// 防止两个延迟接近的模型因统计抖动而来回切换（重建 agent 也有成本）。
const emaSwitchMargin = 0.15

// latencyBestLocked 返回 EMA 延迟最低且未被熔断的模型及其 EMA 值。
// 尚无采样数据的模型延迟未知，不参与选择；全部无数据时返回 false。
// 被熔断（open/半开许可耗尽/hard 冷却中）的模型跳过，避免把流量切向不可用模型。
// 调用方必须已持有 a.mu（读锁或写锁），方法内不再加锁。
// 注意锁序固定为 a.mu → circuit 内部锁，与 recordStats 一致，无死锁风险。
func (a *EinoAgentAdapter) latencyBestLocked() (modelEntry, time.Duration, bool) {
	best := modelEntry{}
	bestEMA := time.Duration(0)
	for _, m := range a.models {
		if !a.circuits.Available(m.name) {
			continue
		}
		stats, ok := a.stats[m.name]
		if !ok || stats.emaLatency <= 0 {
			continue
		}
		if bestEMA == 0 || stats.emaLatency < bestEMA {
			best, bestEMA = m, stats.emaLatency
		}
	}
	return best, bestEMA, bestEMA > 0
}

// ensureLatencyAgent 在 StrategyLatency 下按各模型 EMA 延迟动态切换主模型。
//
// agent 构建时绑定模型，切换需重建 agent/runner；进行中的请求持有旧引用
// 不受影响，新请求使用新 runner。切换条件加迟滞（emaSwitchMargin）防抖动。
func (a *EinoAgentAdapter) ensureLatencyAgent() {
	if a.strategy != StrategyLatency {
		return
	}

	// 快路径：读锁下判断是否需要切换，绝大多数请求在此返回
	a.mu.RLock()
	best, bestEMA, ok := a.latencyBestLocked()
	if !ok || best.name == a.models[a.primaryIndex].name {
		a.mu.RUnlock()
		return
	}
	// 当前主模型必已被请求过（failover 模型有数据意味着主模型先尝试过），
	// 仅为防御数据异常：currentEMA 无数据时不设迟滞门槛，允许直接切换
	currentEMA := a.stats[a.models[a.primaryIndex].name].emaLatency
	needSwitch := currentEMA <= 0 ||
		bestEMA < time.Duration(float64(currentEMA)*(1-emaSwitchMargin))
	a.mu.RUnlock()
	if !needSwitch {
		return
	}

	// 慢路径：写锁下双重检查后重建（其他 goroutine 可能已完成切换）
	a.mu.Lock()
	defer a.mu.Unlock()

	best, bestEMA, ok = a.latencyBestLocked()
	if !ok || best.name == a.models[a.primaryIndex].name {
		return
	}

	newIndex := -1
	for i, m := range a.models {
		if m.name == best.name {
			newIndex = i
			break
		}
	}
	if newIndex < 0 {
		return
	}

	// 先记旧值，重建失败时回滚，保证旧 agent 继续服务
	oldIndex, oldName := a.primaryIndex, a.primaryModelName
	a.primaryIndex, a.primaryModelName = newIndex, best.modelName

	agent, err := a.buildADKAgent(a.tools)
	if err != nil {
		a.primaryIndex, a.primaryModelName = oldIndex, oldName
		a.logger.Error("[Latency] Failed to rebuild ADK agent", "model", best.modelName, "error", err)
		return
	}
	a.agent = agent
	a.runner = a.buildRunner(agent)
	a.logger.Info("[Latency] Switched primary model by EMA",
		"from", oldName, "to", best.modelName,
		"old_ema", currentEMA, "new_ema", bestEMA)
}

func (a *EinoAgentAdapter) Chat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (*eino_schema.Message, *ChatUsage, error) {
	if a.agent == nil || a.runner == nil {
		return nil, nil, errors.New("no ADK agent available")
	}

	// 所有模型均被熔断时快速失败，省掉一次必然失败的 LLM 调用；
	// 上层（core/agent）会捕获该错误并走关键词兜底回复
	if a.circuits.AllUnavailable() {
		return nil, nil, errors.New("all LLM models are circuit-broken")
	}

	// Latency 策略：请求前按 EMA 延迟检查是否需要切换主模型
	a.ensureLatencyAgent()

	chatOpts := a.parseChatOptions(opts)
	runOpts := a.buildRunOptions(chatOpts)
	inputAttrs := a.buildInputAttributes(ctx, messages)

	ctx, span := observability.StartLLMSpan(ctx, a.primaryModelName, inputAttrs...)
	defer span.End()
	span.SetAttributes(
		observability.GenAIRequestMaxTokens.Int(chatOpts.MaxTokens),
		observability.GenAIRequestTemperature.Float64(float64(chatOpts.Temperature)),
	)

	startTime := time.Now()
	finalMsg, lastErr := a.runADK(ctx, messages, runOpts)

	latency := time.Since(startTime)
	modelName := a.getCurrentModelName(finalMsg)
	usage := a.extractUsage(finalMsg)
	// recordStats 需要真实 error 做熔断分类：无错误但也无响应（空流）视为一次失败
	statsErr := lastErr
	if statsErr == nil && finalMsg == nil {
		statsErr = errors.New("no response from ADK agent")
	}
	a.recordStats(modelName, latency, statsErr, &usage)

	if lastErr != nil {
		a.logger.Error("[Chat] ADK agent run failed",
			"error", lastErr,
			"primary_model", a.primaryModelName,
			"final_model", modelName, // 通常是失败前实际调到的模型；无 finalMsg 时为 unknownModelName
			"latency", latency)
		span.SetAttributes(
			observability.GenAIErrorType.String("generation_failure"),
			observability.GenAIErrorMessage.String(lastErr.Error()),
		)
		return nil, nil, lastErr
	}

	if finalMsg == nil {
		a.logger.Error("[Chat] No response from ADK agent", "latency", latency)
		span.SetAttributes(
			observability.GenAIErrorType.String("no_response"),
			observability.GenAIErrorMessage.String("no response from ADK agent"),
		)
		return nil, nil, errors.New("no response from ADK agent")
	}

	outputAttrs := a.buildOutputAttributes(finalMsg, usage)
	span.SetAttributes(outputAttrs...)

	return finalMsg, &usage, nil
}

func (a *EinoAgentAdapter) parseChatOptions(opts []ChatOption) *ChatOptions {
	chatOpts := &ChatOptions{}
	for _, o := range opts {
		o(chatOpts)
	}
	return chatOpts
}

func (a *EinoAgentAdapter) buildRunOptions(chatOpts *ChatOptions) []eino_adk.AgentRunOption {
	modelOpts := []eino_model.Option{
		eino_model.WithTemperature(chatOpts.Temperature),
		eino_model.WithMaxTokens(chatOpts.MaxTokens),
	}
	return []eino_adk.AgentRunOption{
		eino_adk.WithChatModelOptions(modelOpts),
	}
}

func (a *EinoAgentAdapter) buildInputAttributes(ctx context.Context, messages []*eino_schema.Message) []attribute.KeyValue {
	attrs := a.buildMessageAttributes(messages)
	if sessionID, ok := contextutil.SessionIDFromContext(ctx); ok && sessionID != "" {
		attrs = append(attrs, observability.SessionID.String(sessionID))
	}
	return attrs
}

func (a *EinoAgentAdapter) runADK(ctx context.Context, messages []*eino_schema.Message, runOpts []eino_adk.AgentRunOption) (*eino_schema.Message, error) {
	iter := a.runner.Run(ctx, messages, runOpts...)

	var finalMsg *eino_schema.Message
	var lastErr error

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			lastErr = event.Err
			// ADK 把节点执行错误统一包成 NodeRunError，event 自身携带的 AgentName / RunPath
			// 能进一步定位错误来源（哪个 agent / 哪条执行路径）。RunPath 对 ChatModelAgent
			// 通常是 trivial（空切片），但对 AgentTool / 子 agent 场景才有实质内容。
			a.logger.Error("[Chat] ADK event error",
				"error", event.Err,
				"primary_model", a.primaryModelName,
				"agent_name", event.AgentName,
				"run_path", formatRunPath(event.RunPath))
			continue
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			msgVariant := event.Output.MessageOutput
			if m, err := msgVariant.GetMessage(); err == nil && m != nil {
				finalMsg = m
			}
		}
	}

	return finalMsg, lastErr
}

// formatRunPath 将 ADK 事件携带的执行路径格式化为字符串，便于日志可读。
// RunStep.String() 是指针接收者，需要对每个元素取址后调用；空切片返回 "<empty>"。
func formatRunPath(path []eino_adk.RunStep) string {
	if len(path) == 0 {
		return "<empty>"
	}
	parts := make([]string, 0, len(path))
	for i := range path {
		parts = append(parts, path[i].String())
	}
	return strings.Join(parts, " -> ")
}

func (a *EinoAgentAdapter) buildMessageAttributes(messages []*eino_schema.Message) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	var inputMsgs []string
	for i, msg := range messages {
		role := string(msg.Role)
		content := msg.Content
		if content == "" {
			content = "<empty>"
		}
		inputMsgs = append(inputMsgs, fmt.Sprintf("[%s] %s", role, content))
		attrs = append(attrs,
			attribute.String(fmt.Sprintf("gen_ai.message.%d.role", i), role),
			attribute.String(fmt.Sprintf("gen_ai.message.%d.content", i), content),
		)
		if msg.Role == eino_schema.System {
			attrs = append(attrs, observability.GenAISystem.String(content))
		}
	}
	attrs = append(attrs, observability.GenAIPrompt.String(strings.Join(inputMsgs, "\n")))
	attrs = append(attrs, observability.LangfuseObservationInput.String(strings.Join(inputMsgs, "\n")))
	return attrs
}

func (a *EinoAgentAdapter) buildOutputAttributes(msg *eino_schema.Message, usage ChatUsage) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	if msg.Content != "" {
		attrs = append(attrs,
			observability.GenAIMessageRole.String(string(msg.Role)),
			observability.GenAIMessageContent.String(msg.Content),
			observability.GenAIMessageContentType.String("text"),
			observability.GenAICompletion.String(msg.Content),
			observability.LangfuseObservationOutput.String(msg.Content),
		)
	}

	attrs = append(attrs,
		observability.GenAIRequestInputTokenCount.Int(usage.PromptTokens),
		observability.GenAIRequestOutputTokenCount.Int(usage.CompletionTokens),
		observability.GenAIRequestTotalTokenCount.Int(usage.TotalTokens),
	)

	if msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason != "" {
		attrs = append(attrs, observability.GenAIResponseFinishReason.String(msg.ResponseMeta.FinishReason))
	}

	return attrs
}

type adkEventStream struct {
	streamChan <-chan *StreamResult
	done       chan struct{}
	mu         sync.Mutex
	closed     bool
}

func (s *adkEventStream) Recv() (*StreamEvent, error) {
	result, ok := <-s.streamChan
	if !ok {
		return nil, io.EOF
	}
	if result.Err != nil {
		return nil, result.Err
	}
	return result.Event, nil
}

func (s *adkEventStream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
}

func (a *EinoAgentAdapter) StreamChat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (EventStream, error) {
	if a.agent == nil || a.runner == nil {
		return nil, errors.New("no ADK agent available")
	}

	// 与 Chat 一致：全部模型熔断时快速失败，上层走关键词兜底
	if a.circuits.AllUnavailable() {
		return nil, errors.New("all LLM models are circuit-broken")
	}

	// Latency 策略：请求前按 EMA 延迟检查是否需要切换主模型
	a.ensureLatencyAgent()

	chatOpts := a.parseChatOptions(opts)
	runOpts := a.buildRunOptions(chatOpts)
	inputAttrs := a.buildInputAttributes(ctx, messages)

	// origCtx 保存 StartLLMSpan 之前的 ctx（仅含 LLM.StreamChat），
	// 用于创建工具 span 和第二次 llm.chat span，避免后续 span 挂在已 End 的第一个 llm.chat 下
	origCtx := ctx
	ctx, firstLLMSpan := observability.StartLLMSpan(ctx, a.primaryModelName, inputAttrs...)
	firstLLMSpan.SetAttributes(
		observability.GenAIRequestMaxTokens.Int(chatOpts.MaxTokens),
		observability.GenAIRequestTemperature.Float64(float64(chatOpts.Temperature)),
	)

	iter := a.runner.Run(ctx, messages, runOpts...)

	streamChan := make(chan *StreamResult, streamResultBuffer)
	done := make(chan struct{})

	go a.runStream(origCtx, firstLLMSpan, iter, streamChan, done)

	return &adkEventStream{streamChan: streamChan, done: done}, nil
}

// adkIterator 抽象 eino ADK 事件迭代器，便于解耦与测试。
type adkIterator interface {
	Next() (*eino_adk.AgentEvent, bool)
}

// streamState 维护 StreamChat 单次调用的可变状态，避免所有状态散落在主 goroutine 中。
type streamState struct {
	adapter          *EinoAgentAdapter
	ctx              context.Context
	origCtx          context.Context // 仅含 LLM.StreamChat 的原始 ctx，用于创建工具/第二次模型 span
	firstLLMSpan     trace.Span      // 第一次 llm.chat，工具结果到达（第一次调用真正结束）时 End
	modelName        string
	usage            *ChatUsage
	finalMsg         *eino_schema.Message
	isFirstModelCall bool
	firstLLMEnded    bool                   // 标记 firstLLMSpan 是否已 End
	firstModelTCs    []eino_schema.ToolCall // 流式 tool_calls 增量分片的累积结果，End 时写完整 Output
	toolPhaseDone    bool
	toolCallSpan     trace.Span
	secondModelSpan  trace.Span
	secondModelOut   strings.Builder
	secondModelFinal *eino_schema.Message
	startTime        time.Time
	lastEventErr     error // 流式过程中最后一个事件错误；模型失败曾在此被静默吞掉导致统计失真，现供 finish 落熔断统计
}

func newStreamState(a *EinoAgentAdapter, origCtx context.Context, firstLLMSpan trace.Span) *streamState {
	return &streamState{
		adapter:          a,
		ctx:              origCtx, // 初始 ctx 就是 origCtx，后续 ensureToolCallSpan/startSecondModelSpan 会基于它更新
		origCtx:          origCtx,
		firstLLMSpan:     firstLLMSpan,
		isFirstModelCall: true,
		startTime:        time.Now(),
	}
}

// closeSpans 统一兜底关闭未及时 End 的 span，防止 goroutine 各分支重复处理。
func (s *streamState) closeSpans() {
	s.endFirstLLMSpan(s.firstModelTCs)
	if s.toolCallSpan != nil {
		s.toolCallSpan.End()
		s.toolCallSpan = nil
	}
	s.endSecondModelSpan()
}

// endFirstLLMSpan 结束第一次 llm.chat span（finish_reason=tool_calls 到达时调用，handleToolResult/closeSpans 兜底）。
// 用累积完整的 firstModelTCs 写 Output，保证流式分片的 arguments 已拼接完整。
func (s *streamState) endFirstLLMSpan(tcs []eino_schema.ToolCall) {
	if s.firstLLMEnded || s.firstLLMSpan == nil {
		return
	}
	if out := formatToolCallOutput(tcs); out != "" {
		s.firstLLMSpan.SetAttributes(
			observability.GenAICompletion.String(out),
			observability.LangfuseObservationOutput.String(out),
		)
	}
	s.firstLLMSpan.End()
	s.firstLLMSpan = nil
	s.firstLLMEnded = true
}

// accumulateFirstModelToolCalls 累积第一次模型调用的 tool_calls。
//
// OpenAI 流式协议下 arguments 按增量分片传输（首个 delta 只有 index/name，
// 后续 delta 才逐段补齐 JSON），必须等全部 delta 到齐参数才完整。
// 收尾时机由 finish_reason=tool_calls 触发（handleStreamChunk/handleAssistantMessage），
// handleToolResult/closeSpans 仅作兜底。
func (s *streamState) accumulateFirstModelToolCalls(deltas []eino_schema.ToolCall) {
	for _, d := range deltas {
		// Index 为指针，缺失时默认并入第 0 个工具调用
		idx := 0
		if d.Index != nil {
			idx = *d.Index
		}
		for len(s.firstModelTCs) <= idx {
			s.firstModelTCs = append(s.firstModelTCs, eino_schema.ToolCall{})
		}
		tc := &s.firstModelTCs[idx]
		if tc.ID == "" {
			tc.ID = d.ID
		}
		if tc.Function.Name == "" {
			tc.Function.Name = d.Function.Name
		}
		tc.Function.Arguments += d.Function.Arguments
	}
}

func (s *streamState) endSecondModelSpan() {
	if s.secondModelSpan == nil {
		return
	}
	if s.secondModelOut.Len() > 0 {
		out := s.secondModelOut.String()
		s.secondModelSpan.SetAttributes(
			observability.GenAICompletion.String(out),
			observability.LangfuseObservationOutput.String(out),
		)
	} else if s.secondModelFinal != nil && s.secondModelFinal.Content != "" {
		usage := s.adapter.extractUsage(s.secondModelFinal)
		attrs := s.adapter.buildOutputAttributes(s.secondModelFinal, usage)
		s.secondModelSpan.SetAttributes(attrs...)
	}
	s.secondModelSpan.End()
	s.secondModelSpan = nil
}

func (s *streamState) setModelName(msg *eino_schema.Message) {
	if s.modelName == "" {
		s.modelName = s.adapter.getCurrentModelName(msg)
	}
}

func (s *streamState) setUsage(msg *eino_schema.Message) {
	s.usage = &ChatUsage{}
	*s.usage = s.adapter.extractUsage(msg)
}

func (s *streamState) ensureToolCallSpan(tcs []eino_schema.ToolCall) {
	if s.toolCallSpan != nil || len(tcs) == 0 {
		return
	}
	toolNames := collectToolNames(tcs)
	if len(toolNames) == 0 {
		return
	}
	// 用 origCtx 创建，确保工具 span 直接挂在 LLM.StreamChat 下，不受已 End 的 firstLLMSpan 影响
	_, s.toolCallSpan = observability.StartChildSpan(s.origCtx, "Eino.Tool."+strings.Join(toolNames, "."))
	s.toolCallSpan.SetAttributes(
		attribute.String("tool.names", strings.Join(toolNames, ",")),
		attribute.Int("tool.count", len(tcs)),
	)
}

// handleToolResult 处理工具结果消息（Role==Tool）：收尾 toolCallSpan 并标记工具阶段结束。
//
// 工具结果消息由 ADK ReAct 循环内部消化、不转发前端，但其到达时间是
// 「工具执行完毕」的唯一可观测信号：据此结束 toolCallSpan，把工具调用耗时
// 精确收敛到「模型决策→工具结果」区间（方案 c），并置 toolPhaseDone，
// 供第二次模型首条消息到达时即建 secondModelSpan（方案 b），不再等 content。
func (s *streamState) handleToolResult(msg *eino_schema.Message) {
	// 兜底：provider 未回传 finish_reason 时，finish_reason 分支不会创建工具 span，
	// 此处补建（内部幂等；正常路径 toolCallSpan 已存在则直接跳过）
	s.ensureToolCallSpan(s.firstModelTCs)

	// 工具结果到达 = 第一次模型调用（含 tool_calls 增量分片）真正结束：
	// 此时累积的 arguments 已完整，收尾 firstLLMSpan 并写入完整工具决策 Output。
	// 必须在 toolCallSpan 判空之前执行，避免兜底路径漏 End。
	s.endFirstLLMSpan(s.firstModelTCs)

	if s.toolCallSpan == nil {
		return
	}
	if msg != nil {
		s.toolCallSpan.SetAttributes(
			attribute.Int("tool.result.content_len", len(msg.Content)),
		)
	}
	s.toolCallSpan.End()
	s.toolCallSpan = nil
	s.toolPhaseDone = true
}

func (s *streamState) startSecondModelSpan() {
	if s.secondModelSpan != nil || s.isFirstModelCall {
		return
	}
	inputText := "[user] tool result summary"
	// 用 origCtx 创建，确保 secondModelSpan 直接挂在 LLM.StreamChat 下
	_, s.secondModelSpan = observability.StartLLMSpan(s.origCtx, s.modelName,
		observability.GenAIPrompt.String(inputText),
		observability.LangfuseObservationInput.String(inputText),
	)
}

func (s *streamState) recordSecondModelOutput(content string, msg *eino_schema.Message) {
	// 仅记录工具结果到达后的输出（即第二次模型），避免第一次模型的 tool-call
	// chunk 内容泄漏进 secondModelOut，造成 span 输出与时间窗口错位
	if !s.toolPhaseDone {
		return
	}
	s.secondModelOut.WriteString(content)
	s.secondModelFinal = msg
}

func (s *streamState) emitEvent(out chan<- *StreamResult, done <-chan struct{}, event *StreamEvent) bool {
	select {
	case out <- &StreamResult{Event: event}:
		return true
	case <-s.ctx.Done():
		return false
	case <-done:
		return false
	}
}

func (s *streamState) emitError(out chan<- *StreamResult, done <-chan struct{}, err error) bool {
	select {
	case out <- &StreamResult{Err: err}:
		return true
	case <-s.ctx.Done():
		return false
	case <-done:
		return false
	}
}

func (s *streamState) handleAssistantMessage(msg *eino_schema.Message, out chan<- *StreamResult, done <-chan struct{}) bool {
	s.setModelName(msg)
	s.finalMsg = msg
	s.setUsage(msg)

	// 工具阶段结束后到达的首条非流式 assistant 消息即第二次模型调用起点，立即建 span（方案 b）
	if s.toolPhaseDone {
		s.startSecondModelSpan()
	}

	// 第一次模型调用阶段的 ToolCalls（含后续 arguments 增量 delta）全部累积；
	// toolPhaseDone 之后到达的属于下一次模型调用，不再并入
	if len(msg.ToolCalls) > 0 && !s.toolPhaseDone {
		s.isFirstModelCall = false
		s.accumulateFirstModelToolCalls(msg.ToolCalls)
	}

	// 非流式消息一次性完整：finish_reason=tool_calls 即参数到齐，立即收尾
	// firstLLMSpan 并创建工具 span，二者无缝衔接、耗时互不重叠
	if s.firstLLMSpan != nil && len(s.firstModelTCs) > 0 &&
		msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason == "tool_calls" {
		s.endFirstLLMSpan(s.firstModelTCs)
		s.ensureToolCallSpan(s.firstModelTCs)
	}

	if msg.ReasoningContent != "" {
		if !s.emitEvent(out, done, &StreamEvent{
			Type:             StreamEventTypeChunk,
			ReasoningContent: msg.ReasoningContent,
			IsThinking:       true,
			Model:            s.modelName,
		}) {
			return false
		}
	}

	if msg.Content != "" {
		s.recordSecondModelOutput(msg.Content, msg)
		if !s.emitEvent(out, done, &StreamEvent{
			Type:    StreamEventTypeChunk,
			Content: msg.Content,
			Model:   s.modelName,
		}) {
			return false
		}
	}

	return true
}

func (s *streamState) handleStreamingMessage(stream *eino_schema.StreamReader[*eino_schema.Message], out chan<- *StreamResult, done <-chan struct{}) bool {
	defer stream.Close()

	// 工具阶段结束后进入的下一个流即为第二次模型调用：在消费 chunk 前先建 span，
	// 避免原先等 content 非空才建导致首个 chunk 仅 reasoning 时漏计思考时间（方案 b）
	if s.toolPhaseDone {
		s.startSecondModelSpan()
	}

	for {
		select {
		case <-s.ctx.Done():
			return false
		case <-done:
			return false
		default:
		}

		chunk, err := stream.Recv()
		if err == io.EOF {
			return s.handleStreamEOF(chunk, out, done)
		}
		if err != nil {
			s.emitError(out, done, err)
			return false
		}

		if !s.handleStreamChunk(chunk, out, done) {
			return false
		}
	}
}

func (s *streamState) handleStreamEOF(chunk *eino_schema.Message, out chan<- *StreamResult, done <-chan struct{}) bool {
	if chunk != nil {
		s.finalMsg = chunk
		s.setModelName(chunk)
	}

	// 第二次模型流结束 → End secondModelSpan（第一次模型流的 EOF 由 closeSpans 兜底）
	if s.toolPhaseDone && s.secondModelSpan != nil {
		s.endSecondModelSpan()
	}

	var finishReason string
	if chunk != nil && chunk.ResponseMeta != nil {
		finishReason = chunk.ResponseMeta.FinishReason
	}

	return s.emitEvent(out, done, &StreamEvent{
		Type:         StreamEventTypeChunk,
		Content:      "",
		FinishReason: finishReason,
		Model:        s.modelName,
		Usage:        s.usage,
	})
}

func (s *streamState) handleStreamChunk(chunk *eino_schema.Message, out chan<- *StreamResult, done <-chan struct{}) bool {
	s.setModelName(chunk)
	s.setUsage(chunk)

	// 流式 tool_calls 分多次 delta 到达：首个含 name，其余只带 arguments 片段，
	// 累积至 finish_reason=tool_calls 后参数完整（下方判断处统一收尾）
	if len(chunk.ToolCalls) > 0 && !s.toolPhaseDone {
		s.isFirstModelCall = false
		s.accumulateFirstModelToolCalls(chunk.ToolCalls)
	}

	// finish_reason=tool_calls 到达表示第一次模型输出结束、所有 arguments 分片已传完：
	// 1) 立即收尾 firstLLMSpan，耗时精确等于「模型决策」，不再覆盖后续工具执行；
	// 2) 此刻才创建工具 span——与 llm.chat 的 End 无缝衔接、零重叠
	//    （若在首个 delta 就建，会把「模型吐参数的尾巴」算进工具耗时）。
	// 注意必须在下方 Content=="" 提前返回之前判断（tool_calls 的 chunk 通常无文本内容）
	if s.firstLLMSpan != nil && len(s.firstModelTCs) > 0 &&
		chunk.ResponseMeta != nil && chunk.ResponseMeta.FinishReason == "tool_calls" {
		s.endFirstLLMSpan(s.firstModelTCs)
		s.ensureToolCallSpan(s.firstModelTCs)
	}

	isThinking := chunk.ReasoningContent != "" && chunk.Content == ""

	if chunk.ReasoningContent != "" {
		if !s.emitEvent(out, done, &StreamEvent{
			Type:             StreamEventTypeChunk,
			ReasoningContent: chunk.ReasoningContent,
			IsThinking:       true,
			Model:            s.modelName,
		}) {
			return false
		}
	}

	if chunk.Content == "" {
		return true
	}

	s.recordSecondModelOutput(chunk.Content, chunk)

	var finishReason string
	if chunk.ResponseMeta != nil {
		finishReason = chunk.ResponseMeta.FinishReason
	}

	toolCalls := buildToolCalls(chunk.ToolCalls)

	return s.emitEvent(out, done, &StreamEvent{
		Type:         StreamEventTypeChunk,
		Content:      chunk.Content,
		FinishReason: finishReason,
		Model:        s.modelName,
		Usage:        s.usage,
		ToolCalls:    toolCalls,
		IsThinking:   isThinking,
	})
}

func (s *streamState) finish(out chan<- *StreamResult, done <-chan struct{}) {
	s.emitEvent(out, done, &StreamEvent{
		Type:       StreamEventTypeAction,
		ActionType: "exit",
		Model:      s.modelName,
		Usage:      s.usage,
	})

	if s.modelName != "" {
		latency := time.Since(s.startTime)
		// 流式过程中的事件错误（模型调用失败）此前被静默吞掉、统一记为成功，
		// 导致熔断器与统计失真；此处补记真实错误
		s.adapter.recordStats(s.modelName, latency, s.lastEventErr, s.usage)
	}

	if s.finalMsg == nil {
		return
	}

	if s.usage == nil {
		s.setUsage(s.finalMsg)
	}

	outputAttrs := s.adapter.buildOutputAttributes(s.finalMsg, *s.usage)
	if len(outputAttrs) == 0 && len(s.finalMsg.ToolCalls) > 0 {
		toolNames := collectToolNames(s.finalMsg.ToolCalls)
		outputAttrs = []attribute.KeyValue{
			observability.GenAICompletion.String("[tool_call] " + strings.Join(toolNames, ", ")),
			observability.LangfuseObservationOutput.String("[tool_call] " + strings.Join(toolNames, ", ")),
		}
	}

	// 根据场景选择 span 设置 Output：有 secondModelSpan 说明是二次模型调用，否则是首次模型直接回复
	if s.secondModelSpan != nil {
		s.secondModelSpan.SetAttributes(outputAttrs...)
	} else if s.firstLLMSpan != nil {
		s.firstLLMSpan.SetAttributes(outputAttrs...)
	}
}

// runStream 是 EinoAgentAdapter 流式对话的核心事件循环。
//
// 设计要点：
//   - 以 select + default 抢占式轮询 ctx.Done() 与 done，避免在迭代器卡住时无法响应取消/结束信号。
//   - 通过 streamState 统一管理本次流的生命周期（trace 子 span、模型名、用量、首/次模型调用标记），
//     退出时由 defer 统一收尾，保证 span 与 channel 必被关闭，杜绝泄漏。
//   - 迭代器事件分派：非流式助手消息走 handleAssistantMessage，流式块走 handleStreamingMessage；
//     工具结果消息（Role==Tool）由框架内部消化、不向下游转发，但据此收尾 toolCallSpan 以精确记录工具调用耗时。
//   - iter.Next() 返回 false 表示 ReAct 循环结束，调用 state.finish 发送 exit 动作并落库统计。
//   - firstLLMSpan 在第一次模型输出 tool_calls 时 End（endFirstLLMSpan），secondModelSpan 在第二次模型流 EOF 时 End，
//     closeSpans 作为兜底。origCtx 仅含 LLM.StreamChat，用于创建工具/第二次模型 span，避免挂在已 End 的 firstLLMSpan 下。
//
// 返回即代表本次流式对话彻底结束，out channel 随之关闭。
func (a *EinoAgentAdapter) runStream(origCtx context.Context, firstLLMSpan trace.Span, iter adkIterator, out chan<- *StreamResult, done <-chan struct{}) {
	// 构建本次流的共享状态（span 生命周期由 streamState 管理，origCtx 用于后续 span 创建）
	state := newStreamState(a, origCtx, firstLLMSpan)

	// defer 顺序与执行顺序相反：先 closeSpans 兜底未 End 的 span，最后关 out channel，确保消费方读到 EOF 后再收尾
	defer state.closeSpans()
	defer close(out)

	for {
		// 抢占式取消检查：每轮迭代优先消费 ctx.Done()/done 信号，避免 iter.Next() 阻塞时无法响应取消
		select {
		case <-origCtx.Done():
			return
		case <-done:
			return
		default:
		}

		// 拉取下一个 ReAct 事件；ok==false 表示整条 Agent 链路迭代完毕
		event, ok := iter.Next()
		if !ok {
			// 正常结束：发送 exit 动作、落库统计、补齐用量与 Output 属性
			state.finish(out, done)
			return
		}

		// 流式分支的事件错误不返回给调用方（continue 推进后续事件，避免单点错误中断整条流），
		// 但仍要打日志，否则下次出错时流式分支比同步 Chat 更难排查——只看日志根本不知道出过错。
		// 与 runADK（同步分支）保持字段一致，便于统一检索。
		if event.Err != nil {
			a.logger.Error("[Stream] ADK event error",
				"error", event.Err,
				"primary_model", a.primaryModelName,
				"agent_name", event.AgentName,
				"run_path", formatRunPath(event.RunPath))
			state.lastEventErr = event.Err // 供 finish 落熔断统计（事件仍 continue 推进，不中断流）
			continue
		}

		// 过滤无消息载荷的事件（如纯状态变更），不影响下游
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		msgVariant := event.Output.MessageOutput
		// 工具结果消息（Role==Tool）由框架内部消化、不转发前端，但其到达是
		// 「工具执行完毕」的可观测信号：据此收尾 toolCallSpan 并标记工具阶段结束（方案 a/c）
		if msgVariant.Role == eino_schema.Tool {
			state.handleToolResult(msgVariant.Message)
			continue
		}

		// 非流式助手消息：一次性完整回复（如 fallback 或非流式模型），直接转发整条消息
		if msgVariant.Message != nil && msgVariant.Message.Role == eino_schema.Assistant && !msgVariant.IsStreaming {
			if !state.handleAssistantMessage(msgVariant.Message, out, done) {
				return
			}
			continue
		}

		// 流式助手消息：逐 chunk 转发，handleStreamingMessage 内部会循环 Recv 直到 EOF
		if msgVariant.IsStreaming && msgVariant.MessageStream != nil {
			if !state.handleStreamingMessage(msgVariant.MessageStream, out, done) {
				return
			}
		}
	}
}

func collectToolNames(tcs []eino_schema.ToolCall) []string {
	var names []string
	for _, tc := range tcs {
		if tc.Function.Name != "" {
			names = append(names, tc.Function.Name)
		}
	}
	return names
}

// formatToolCallOutput 将第一次模型调用的工具决策格式化为 Output 文本，
// 如 `[tool_call] get_weather({"city":"北京"})`，多工具以分号分隔。
// 模型返回的 Arguments 本身就是 JSON 字符串，原样展示即可还原参数细节。
func formatToolCallOutput(tcs []eino_schema.ToolCall) string {
	if len(tcs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[tool_call]")
	for _, tc := range tcs {
		b.WriteString(" " + tc.Function.Name + "(" + strings.TrimSpace(tc.Function.Arguments) + ");")
	}
	return strings.TrimSuffix(b.String(), ";")
}

func buildToolCalls(tcs []eino_schema.ToolCall) []ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	toolCalls := make([]ToolCall, 0, len(tcs))
	for _, tc := range tcs {
		if tc.Function.Name == "" {
			continue
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:       tc.ID,
			ToolName: tc.Function.Name,
			Params:   parseToolCallParams(tc.Function.Arguments),
		})
	}
	return toolCalls
}

func parseToolCallParams(arguments string) map[string]any {
	if arguments == "" {
		return nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(arguments), &params); err != nil {
		return map[string]any{"arguments": arguments}
	}
	return params
}

func (a *EinoAgentAdapter) getCurrentModelName(msg *eino_schema.Message) string {
	if msg != nil && msg.Extra != nil {
		if name, ok := msg.Extra["model_name"].(string); ok && name != "" {
			return name
		}
	}
	if a.primaryModelName != "" {
		return a.primaryModelName
	}
	return unknownModelName
}

func (a *EinoAgentAdapter) extractUsage(msg *eino_schema.Message) ChatUsage {
	model := a.getCurrentModelName(msg)
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return ChatUsage{Model: model}
	}
	return ChatUsage{
		PromptTokens:     int(msg.ResponseMeta.Usage.PromptTokens),
		CompletionTokens: int(msg.ResponseMeta.Usage.CompletionTokens),
		TotalTokens:      int(msg.ResponseMeta.Usage.TotalTokens),
		Model:            model,
	}
}

// recordStats 记录单次模型调用的统计与熔断结果。
// err 为 nil 表示成功；非 nil 时按 classifyModelError 分类计入对应模型的熔断器。
func (a *EinoAgentAdapter) recordStats(modelName string, latency time.Duration, err error, usage *ChatUsage) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// LLM 响应返回的模型名是原始名（如含 . /），需归一化为内部 stats key
	if key, ok := a.nameToKey[modelName]; ok {
		modelName = key
	}

	// 失败归属：同步路径拿不到 finalMsg 时 modelName 为 unknown（failover 后无法确定
	// 具体是哪一环失败），主模型必然被尝试过，将其保守记到主模型头上。
	// 若错误实为配额类且主备同账号（当前配置即如此），一并熔断无偏差。
	isError := err != nil
	if isError && modelName == unknownModelName {
		if key, ok := a.nameToKey[a.primaryModelName]; ok {
			modelName = key
		}
	}

	if _, ok := a.stats[modelName]; !ok {
		a.stats[modelName] = &modelStats{}
	}
	stats := a.stats[modelName]
	stats.requestCount++
	stats.totalLatency += latency
	stats.updateEMA(latency)
	if isError {
		stats.errorCount++
	}

	// 熔断记录放在锁外会导致状态窗口（stats 更新完、熔断未记），
	// manager/modelCircuit 内部有自己的锁，此处嵌套持锁无死锁风险（锁序固定：a.mu → circuit.mu）
	a.circuits.Record(modelName, err)

	status := "success"
	if isError {
		status = "error"
	}
	observability.LLMRequestsTotal.WithLabelValues(modelName, status).Inc()
	observability.LLMRequestDuration.WithLabelValues(modelName).Observe(latency.Seconds())

	if usage != nil && usage.Model != "" {
		observability.LLMRequestTokens.WithLabelValues(usage.Model).Add(float64(usage.PromptTokens))
		observability.LLMCompletionTokens.WithLabelValues(usage.Model).Add(float64(usage.CompletionTokens))
	}
}

func sanitizeProviderName(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "/", "-"), ".", "-")
}

func parseStrategy(strategy string) Strategy {
	switch strings.ToLower(strategy) {
	case "cost":
		return StrategyCost
	case "latency":
		return StrategyLatency
	case "capability":
		return StrategyCapability
	case "fallback":
		return StrategyFallback
	case "weighted":
		return StrategyWeighted
	case "fixed":
		return StrategyFixed
	default:
		return StrategyFallback
	}
}
