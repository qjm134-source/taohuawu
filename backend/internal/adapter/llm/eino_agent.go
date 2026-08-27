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
	capabilityMap    map[string][]string
	timeout          time.Duration
	primaryModelName string
	primaryIndex     int
	maxRetries       int
}

func NewEinoAgentAdapter(logger logging.Logger, cfg config.LLMConfig, tools []eino_tool.InvokableTool) *EinoAgentAdapter {
	_ = tools

	adapter := &EinoAgentAdapter{
		logger:        logger,
		strategy:      parseStrategy(cfg.Strategy),
		weights:       make(map[string]float64),
		stats:         make(map[string]*modelStats),
		capabilityMap: make(map[string][]string),
		timeout:       cfg.Timeout.Duration,
		maxRetries:    cfg.MaxRetries,
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
		adapter.models = append(adapter.models, modelEntry{
			name:      name,
			modelName: mc.Name,
			model:     chatModel,
		})
		adapter.fallback = append(adapter.fallback, name)
		adapter.stats[name] = &modelStats{}

	}

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

func (a *EinoAgentAdapter) getFailoverModel(ctx context.Context, failoverCtx *eino_adk.FailoverContext[*eino_schema.Message]) (eino_model.BaseModel[*eino_schema.Message], []*eino_schema.Message, error) {
	attempt := int(failoverCtx.FailoverAttempt)
	n := len(a.models)

	if n <= 1 || attempt >= n {
		return nil, nil, nil
	}

	idx := (a.primaryIndex + attempt) % n
	if idx == a.primaryIndex {
		return nil, nil, nil
	}

	entry := a.models[idx]

	return entry.model, failoverCtx.InputMessages, nil
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

func (a *EinoAgentAdapter) Chat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (*eino_schema.Message, *ChatUsage, error) {
	if a.agent == nil || a.runner == nil {
		return nil, nil, errors.New("no ADK agent available")
	}

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
	a.recordStats(modelName, latency, lastErr != nil || finalMsg == nil, &usage)

	if lastErr != nil {
		a.logger.Error("[Chat] ADK agent run failed", "error", lastErr, "latency", latency)
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
			a.logger.Error("[Chat] ADK event error", "error", event.Err)
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
		s.adapter.recordStats(s.modelName, latency, false, s.usage)
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

		// 框架内部错误事件静默丢弃，交由后续事件继续推进，避免单点错误中断整条流
		if event.Err != nil {
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

func (a *EinoAgentAdapter) recordStats(modelName string, latency time.Duration, isError bool, usage *ChatUsage) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.stats[modelName]; !ok {
		a.stats[modelName] = &modelStats{}
	}
	a.stats[modelName].requestCount++
	a.stats[modelName].totalLatency += latency
	if isError {
		a.stats[modelName].errorCount++
	}

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
