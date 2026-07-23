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
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
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
	if sessionID, ok := SessionIDFromContext(ctx); ok && sessionID != "" {
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
	close(s.done)
}

func (a *EinoAgentAdapter) StreamChat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (EventStream, error) {
	if a.agent == nil || a.runner == nil {
		return nil, errors.New("no ADK agent available")
	}

	chatOpts := a.parseChatOptions(opts)
	runOpts := a.buildRunOptions(chatOpts)
	inputAttrs := a.buildInputAttributes(ctx, messages)

	ctx, span := observability.StartLLMSpan(ctx, a.primaryModelName, inputAttrs...)
	span.SetAttributes(
		observability.GenAIRequestMaxTokens.Int(chatOpts.MaxTokens),
		observability.GenAIRequestTemperature.Float64(float64(chatOpts.Temperature)),
	)

	iter := a.runner.Run(ctx, messages, runOpts...)

	streamChan := make(chan *StreamResult, streamResultBuffer)
	done := make(chan struct{})

	go a.runStream(ctx, span, iter, streamChan, done)

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
	span             trace.Span
	modelName        string
	usage            *ChatUsage
	finalMsg         *eino_schema.Message
	isFirstModelCall bool
	toolCallSpan     trace.Span
	secondModelSpan  trace.Span
	secondModelOut   strings.Builder
	secondModelFinal *eino_schema.Message
	startTime        time.Time
}

func newStreamState(a *EinoAgentAdapter, ctx context.Context, span trace.Span) *streamState {
	return &streamState{
		adapter:          a,
		ctx:              ctx,
		span:             span,
		isFirstModelCall: true,
		startTime:        time.Now(),
	}
}

// closeSpans 统一关闭 toolCallSpan 与 secondModelSpan，防止 goroutine 各分支重复处理。
func (s *streamState) closeSpans() {
	if s.toolCallSpan != nil {
		s.toolCallSpan.End()
	}
	s.endSecondModelSpan()
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
	s.ctx, s.toolCallSpan = observability.StartChildSpan(s.ctx, "Eino.Tool."+strings.Join(toolNames, "."))
	s.toolCallSpan.SetAttributes(
		attribute.String("tool.names", strings.Join(toolNames, ",")),
		attribute.Int("tool.count", len(toolNames)),
	)
}

func (s *streamState) startSecondModelSpan() {
	if s.secondModelSpan != nil || s.isFirstModelCall {
		return
	}
	inputText := "[user] tool result summary"
	s.ctx, s.secondModelSpan = observability.StartLLMSpan(s.ctx, s.modelName,
		observability.GenAIPrompt.String(inputText),
		observability.LangfuseObservationInput.String(inputText),
	)
}

func (s *streamState) recordSecondModelOutput(content string, msg *eino_schema.Message) {
	if s.isFirstModelCall {
		return
	}
	s.startSecondModelSpan()
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

	s.ensureToolCallSpan(msg.ToolCalls)

	if s.isFirstModelCall && len(msg.ToolCalls) > 0 {
		s.isFirstModelCall = false
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

	s.ensureToolCallSpan(chunk.ToolCalls)

	if s.isFirstModelCall && len(chunk.ToolCalls) > 0 {
		s.isFirstModelCall = false
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
	s.span.SetAttributes(outputAttrs...)
}

func (a *EinoAgentAdapter) runStream(ctx context.Context, span trace.Span, iter adkIterator, out chan<- *StreamResult, done <-chan struct{}) {
	defer close(out)
	defer span.End()

	state := newStreamState(a, ctx, span)
	defer state.closeSpans()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		default:
		}

		event, ok := iter.Next()
		if !ok {
			state.finish(out, done)
			return
		}

		if event.Err != nil {
			continue
		}

		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		msgVariant := event.Output.MessageOutput
		if msgVariant.Role == eino_schema.Tool {
			continue
		}

		if msgVariant.Message != nil && msgVariant.Message.Role == eino_schema.Assistant && !msgVariant.IsStreaming {
			if !state.handleAssistantMessage(msgVariant.Message, out, done) {
				return
			}
			continue
		}

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
