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

var llmPricing = map[string]struct {
	Input  float64
	Output float64
}{
	"deepseek-chat":     {Input: 0.0001, Output: 0.0002},
	"deepseek-reasoner": {Input: 0.00055, Output: 0.00219},
	"gpt-4o":            {Input: 0.0025, Output: 0.01},
	"gpt-4o-mini":       {Input: 0.00015, Output: 0.0006},
	"gpt-3.5-turbo":     {Input: 0.0005, Output: 0.0015},
	"claude-3-5-sonnet": {Input: 0.003, Output: 0.015},
	"claude-3-haiku":    {Input: 0.00025, Output: 0.00125},
	"qwen-turbo":        {Input: 0.0002, Output: 0.0006},
	"qwen-plus":         {Input: 0.0004, Output: 0.0012},
	"qwen-max":          {Input: 0.002, Output: 0.006},
	"qwen3.5-27b":       {Input: 0.001, Output: 0.003},
	"doubao-pro-32k":    {Input: 0.0008, Output: 0.005},
	"doubao-pro-128k":   {Input: 0.005, Output: 0.009},
	"glm-4":             {Input: 0.01, Output: 0.01},
	"glm-4-flash":       {Input: 0.0001, Output: 0.0001},
}

func calculateLLMCost(model string, inputTokens, outputTokens int) float64 {
	pricing, ok := llmPricing[model]
	if !ok {
		pricing = llmPricing["gpt-3.5-turbo"]
	}
	inputCost := float64(inputTokens) / 1000 * pricing.Input
	outputCost := float64(outputTokens) / 1000 * pricing.Output
	return inputCost + outputCost
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
	logger.Info("[NewEinoAgentAdapter] Creating ADK adapter", "tool_count", len(tools))
	for _, t := range tools {
		info, _ := t.Info(context.Background())
		logger.Info("[NewEinoAgentAdapter] Tool registered", "name", info.Name, "desc", info.Desc)
	}

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

		logger.Info("Eino model registered", "name", name, "model", mc.Name, "base_url", mc.BaseURL)
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
	a.logger.Info("[buildADKAgent] Building ADK agent", "primary_model", primaryModel.name)

	var toolsConfig eino_adk.ToolsConfig
	if len(tools) > 0 {
		a.logger.Info("[buildADKAgent] Registering tools for ADK agent", "tool_count", len(tools))
		baseTools := make([]eino_tool.BaseTool, 0, len(tools))
		for _, t := range tools {
			info, _ := t.Info(context.Background())
			a.logger.Info("[buildADKAgent] Adding tool", "name", info.Name)
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
		MaxIterations:       20,
	}

	agent, err := eino_adk.NewChatModelAgent(context.Background(), config)
	if err != nil {
		a.logger.Error("Failed to create ADK ChatModelAgent", "error", err)
		return nil, err
	}

	a.logger.Info("[buildADKAgent] ADK agent created successfully")
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
	a.logger.Info("ADK failover: selecting next model", "attempt", attempt, "model", entry.name)
	return entry.model, failoverCtx.InputMessages, nil
}

func (a *EinoAgentAdapter) buildRunner(agent *eino_adk.ChatModelAgent) *eino_adk.Runner {
	a.logger.Info("[buildRunner] Creating Runner", "enable_streaming", true)
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

	chatOpts := &ChatOptions{}
	for _, o := range opts {
		o(chatOpts)
	}

	modelOpts := []eino_model.Option{
		eino_model.WithTemperature(chatOpts.Temperature),
		eino_model.WithMaxTokens(chatOpts.MaxTokens),
	}

	runOpts := []eino_adk.AgentRunOption{
		eino_adk.WithChatModelOptions(modelOpts),
	}

	a.logger.Info("[Chat] Starting ADK agent run", "message_count", len(messages))
	startTime := time.Now()

	inputAttrs := a.buildMessageAttributes(messages)

	if sessionID, ok := ctx.Value("session_id").(string); ok && sessionID != "" {
		inputAttrs = append(inputAttrs, observability.SessionID.String(sessionID))
	}

	ctx, span := observability.StartLLMSpan(ctx, a.primaryModelName, inputAttrs...)
	defer span.End()

	span.SetAttributes(
		observability.GenAIRequestMaxTokens.Int(chatOpts.MaxTokens),
		observability.GenAIRequestTemperature.Float64(float64(chatOpts.Temperature)),
	)

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

	usage = a.extractUsage(finalMsg)
	a.logger.Info("[Chat] ADK agent run success", "model", modelName, "latency", latency, "content_len", len(finalMsg.Content))

	outputAttrs := a.buildOutputAttributes(finalMsg, usage)
	span.SetAttributes(outputAttrs...)

	return finalMsg, &usage, nil
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

// getSessionIDFromContext 从 context 中提取 session_id
func (a *EinoAgentAdapter) getSessionIDFromContext(ctx context.Context) string {
	return ctx.Value("session_id").(string)
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
		// Langfuse 专用属性，用于成本计算
		observability.GenAIUsageInputTokens.Int(usage.PromptTokens),
		observability.GenAIUsageOutputTokens.Int(usage.CompletionTokens),
		observability.GenAIUsageTotalTokens.Int(usage.TotalTokens),
		observability.GenAIUsageCost.Float64(usage.Cost),
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

	chatOpts := &ChatOptions{}
	for _, o := range opts {
		o(chatOpts)
	}

	modelOpts := []eino_model.Option{
		eino_model.WithTemperature(chatOpts.Temperature),
		eino_model.WithMaxTokens(chatOpts.MaxTokens),
	}

	runOpts := []eino_adk.AgentRunOption{
		eino_adk.WithChatModelOptions(modelOpts),
	}

	a.logger.Info("[StreamChat] Starting ADK agent run", "message_count", len(messages))

	inputAttrs := a.buildMessageAttributes(messages)

	if sessionID, ok := ctx.Value("session_id").(string); ok && sessionID != "" {
		inputAttrs = append(inputAttrs, observability.SessionID.String(sessionID))
	}

	ctx, span := observability.StartLLMSpan(ctx, a.primaryModelName, inputAttrs...)
	span.SetAttributes(
		observability.GenAIRequestMaxTokens.Int(chatOpts.MaxTokens),
		observability.GenAIRequestTemperature.Float64(float64(chatOpts.Temperature)),
	)

	iter := a.runner.Run(ctx, messages, runOpts...)

	streamChan := make(chan *StreamResult, 100)
	done := make(chan struct{})

	go func(ctx context.Context) {
		defer close(streamChan)
		defer span.End()

		var modelName string
		var usage *ChatUsage
		var finalMsg *eino_schema.Message
		startTime := time.Now()
		isFirstModelCall := true
		var toolCallSpan trace.Span
		var secondModelSpan trace.Span
		var secondModelFinalMsg *eino_schema.Message
		var secondModelOutput strings.Builder

		for {
			select {
			case <-ctx.Done():
				if toolCallSpan != nil {
					toolCallSpan.End()
				}
				a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
				return
			case <-done:
				if toolCallSpan != nil {
					toolCallSpan.End()
				}
				a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
				return
			default:
			}

			event, ok := iter.Next()
			if !ok {
				a.logger.Info("[StreamChat] Iterator closed, exiting")

				if toolCallSpan != nil {
					toolCallSpan.End()
				}
				a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())

				select {
				case streamChan <- &StreamResult{
					Event: &StreamEvent{
						Type:       StreamEventTypeAction,
						ActionType: "exit",
						Model:      modelName,
						Usage:      usage,
					},
				}:
				case <-ctx.Done():
				case <-done:
				}

				if modelName != "" {
					latency := time.Since(startTime)
					a.recordStats(modelName, latency, false, usage)
				}

				if finalMsg != nil {
					if usage == nil {
						usage = &ChatUsage{}
						*usage = a.extractUsage(finalMsg)
					}
					outputAttrs := a.buildOutputAttributes(finalMsg, *usage)
					if len(outputAttrs) == 0 && len(finalMsg.ToolCalls) > 0 {
						var toolNames []string
						for _, tc := range finalMsg.ToolCalls {
							if tc.Function.Name != "" {
								toolNames = append(toolNames, tc.Function.Name)
							}
						}
						outputAttrs = []attribute.KeyValue{
							observability.GenAICompletion.String("[tool_call] " + strings.Join(toolNames, ", ")),
							observability.LangfuseObservationOutput.String("[tool_call] " + strings.Join(toolNames, ", ")),
						}
					}
					a.logger.Info("[StreamChat] Setting output attributes", "attr_count", len(outputAttrs), "content_len", len(finalMsg.Content))
					span.SetAttributes(outputAttrs...)
				}

				return
			}

			a.logger.Debug("[StreamChat] Got event from iterator", "has_output", event.Output != nil)

			if event.Err != nil {
				a.logger.Error("[StreamChat] Event error", "error", event.Err)
				continue
			}

			if event.Output != nil && event.Output.MessageOutput != nil {
				msgVariant := event.Output.MessageOutput

				if msgVariant.Role == eino_schema.Tool {
					a.logger.Info("[StreamChat] Got tool message, skipping", "role", msgVariant.Role)
					continue
				}

				if msgVariant.Message != nil && msgVariant.Message.Role == eino_schema.Assistant && !msgVariant.IsStreaming {
					if modelName == "" {
						modelName = a.getCurrentModelName(msgVariant.Message)
					}
					finalMsg = msgVariant.Message
					usage = &ChatUsage{}
					*usage = a.extractUsage(msgVariant.Message)

					a.logger.Info("[StreamChat] Got non-streaming assistant message",
						"content_len", len(msgVariant.Message.Content),
						"reasoning_len", len(msgVariant.Message.ReasoningContent),
						"tool_calls", len(msgVariant.Message.ToolCalls))

					if len(msgVariant.Message.ToolCalls) > 0 && toolCallSpan == nil {
						var toolNames []string
						for _, tc := range msgVariant.Message.ToolCalls {
							if tc.Function.Name != "" {
								toolNames = append(toolNames, tc.Function.Name)
							}
						}
						ctx, toolCallSpan = observability.StartChildSpan(ctx, "Eino.Tool."+strings.Join(toolNames, "."))
						toolCallSpan.SetAttributes(
							attribute.String("tool.names", strings.Join(toolNames, ",")),
							attribute.Int("tool.count", len(toolNames)),
						)
						a.logger.Info("[StreamChat] Created tool call span", "tools", toolNames)
					}

					if isFirstModelCall && len(msgVariant.Message.ToolCalls) > 0 {
						isFirstModelCall = false
					}

					if msgVariant.Message.ReasoningContent != "" {
						a.logger.Info("[StreamChat] Sending reasoning content", "reasoning_len", len(msgVariant.Message.ReasoningContent))
						select {
						case streamChan <- &StreamResult{
							Event: &StreamEvent{
								Type:             StreamEventTypeChunk,
								ReasoningContent: msgVariant.Message.ReasoningContent,
								IsThinking:       true,
								Model:            modelName,
							},
						}:
							a.logger.Info("[StreamChat] Reasoning content sent")
						case <-ctx.Done():
							return
						case <-done:
							return
						}
					}

					if msgVariant.Message.Content != "" {
						if !isFirstModelCall && secondModelSpan == nil {
							inputText := "[user] tool result summary"
							ctx, secondModelSpan = observability.StartLLMSpan(ctx, modelName,
								observability.GenAIPrompt.String(inputText),
								observability.LangfuseObservationInput.String(inputText),
							)
							a.logger.Info("[StreamChat] Created second model call span", "model", modelName)
						}
						if !isFirstModelCall {
							secondModelOutput.WriteString(msgVariant.Message.Content)
						}
						secondModelFinalMsg = msgVariant.Message

						a.logger.Info("[StreamChat] Sending non-streaming assistant content", "content_len", len(msgVariant.Message.Content))
						select {
						case streamChan <- &StreamResult{
							Event: &StreamEvent{
								Type:    StreamEventTypeChunk,
								Content: msgVariant.Message.Content,
								Model:   modelName,
							},
						}:
							a.logger.Info("[StreamChat] Non-streaming content sent")
						case <-ctx.Done():
							return
						case <-done:
							return
						}
					}
					continue
				}

				if msgVariant.IsStreaming && msgVariant.MessageStream != nil {
					a.logger.Info("[StreamChat] Got streaming message, starting to read chunks")
					stream := msgVariant.MessageStream

					for {
						select {
						case <-ctx.Done():
							stream.Close()
							if toolCallSpan != nil {
								toolCallSpan.End()
							}
							if secondModelSpan != nil {
								a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
							}
							return
						case <-done:
							stream.Close()
							if toolCallSpan != nil {
								toolCallSpan.End()
							}
							if secondModelSpan != nil {
								a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
							}
							return
						default:
						}

						chunk, err := stream.Recv()

						if err == io.EOF {
							stream.Close()

							if chunk != nil {
								finalMsg = chunk
								if modelName == "" {
									modelName = a.getCurrentModelName(chunk)
								}
							}

							var finishReason string
							if chunk != nil && chunk.ResponseMeta != nil {
								finishReason = chunk.ResponseMeta.FinishReason
							}

							if toolCallSpan != nil {
								toolCallSpan.End()
							}
							if secondModelSpan != nil {
								a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
							}

							select {
							case streamChan <- &StreamResult{
								Event: &StreamEvent{
									Type:         StreamEventTypeChunk,
									Content:      "",
									FinishReason: finishReason,
									Model:        modelName,
									Usage:        usage,
								},
							}:
							case <-ctx.Done():
							case <-done:
							}
							break
						}

						if err != nil {
							stream.Close()
							if toolCallSpan != nil {
								toolCallSpan.End()
							}
							if secondModelSpan != nil {
								a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
							}
							select {
							case streamChan <- &StreamResult{Err: err}:
							case <-ctx.Done():
							case <-done:
							}
							return
						}

						if modelName == "" {
							modelName = a.getCurrentModelName(chunk)
						}
						usage = &ChatUsage{}
						*usage = a.extractUsage(chunk)

						var finishReason string
						if chunk.ResponseMeta != nil {
							finishReason = chunk.ResponseMeta.FinishReason
						}

						var toolCalls []ToolCall
						if len(chunk.ToolCalls) > 0 {
							toolCalls = make([]ToolCall, 0, len(chunk.ToolCalls))
							for _, tc := range chunk.ToolCalls {
								if tc.Function.Name == "" {
									continue
								}
								var params map[string]interface{}
								if tc.Function.Arguments != "" {
									if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
										params = map[string]interface{}{"arguments": tc.Function.Arguments}
									}
								}
								toolCalls = append(toolCalls, ToolCall{
									ID:       tc.ID,
									ToolName: tc.Function.Name,
									Params:   params,
								})
							}

							if toolCallSpan == nil {
								var toolNames []string
								for _, tc := range chunk.ToolCalls {
									if tc.Function.Name != "" {
										toolNames = append(toolNames, tc.Function.Name)
									}
								}
								ctx, toolCallSpan = observability.StartChildSpan(ctx, "Eino.Tool."+strings.Join(toolNames, "."))
								toolCallSpan.SetAttributes(
									attribute.String("tool.names", strings.Join(toolNames, ",")),
									attribute.Int("tool.count", len(toolNames)),
								)
								a.logger.Info("[StreamChat] Created tool call span", "tools", toolNames)
							}
						}

						isThinking := chunk.ReasoningContent != "" && chunk.Content == ""

						if isFirstModelCall && len(chunk.ToolCalls) > 0 {
							isFirstModelCall = false
						}

						if chunk.ReasoningContent != "" {
							a.logger.Info("[StreamChat] Sending reasoning chunk", "reasoning_len", len(chunk.ReasoningContent))
							select {
							case streamChan <- &StreamResult{
								Event: &StreamEvent{
									Type:             StreamEventTypeChunk,
									ReasoningContent: chunk.ReasoningContent,
									IsThinking:       true,
									Model:            modelName,
								},
							}:
								a.logger.Info("[StreamChat] Reasoning chunk sent successfully")
							case <-ctx.Done():
								a.logger.Warn("[StreamChat] Context cancelled while sending reasoning")
								stream.Close()
								if toolCallSpan != nil {
									toolCallSpan.End()
								}
								if secondModelSpan != nil {
									a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
								}
								return
							case <-done:
								stream.Close()
								if toolCallSpan != nil {
									toolCallSpan.End()
								}
								if secondModelSpan != nil {
									a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
								}
								return
							}
						}

						if chunk.Content != "" {
							if !isFirstModelCall && secondModelSpan == nil {
								inputText := "[user] tool result summary"
								ctx, secondModelSpan = observability.StartLLMSpan(ctx, modelName,
									observability.GenAIPrompt.String(inputText),
									observability.LangfuseObservationInput.String(inputText),
								)
								a.logger.Info("[StreamChat] Created second model call span", "model", modelName)
							}
							if !isFirstModelCall {
								secondModelOutput.WriteString(chunk.Content)
							}
							secondModelFinalMsg = chunk

							a.logger.Debug("[StreamChat] Sending content chunk", "content_len", len(chunk.Content))
							select {
							case streamChan <- &StreamResult{
								Event: &StreamEvent{
									Type:         StreamEventTypeChunk,
									Content:      chunk.Content,
									FinishReason: finishReason,
									Model:        modelName,
									Usage:        usage,
									ToolCalls:    toolCalls,
									IsThinking:   isThinking,
								},
							}:
								a.logger.Debug("[StreamChat] Content chunk sent")
							case <-ctx.Done():
								stream.Close()
								if toolCallSpan != nil {
									toolCallSpan.End()
								}
								if secondModelSpan != nil {
									a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
								}
								return
							case <-done:
								stream.Close()
								if toolCallSpan != nil {
									toolCallSpan.End()
								}
								if secondModelSpan != nil {
									a.endSecondModelSpan(secondModelSpan, secondModelFinalMsg, secondModelOutput.String())
								}
								return
							}
						}
					}
				}
			}
		}
	}(ctx)

	return &adkEventStream{streamChan: streamChan, done: done}, nil
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
	return "unknown"
}

func (a *EinoAgentAdapter) endSecondModelSpan(span trace.Span, finalMsg *eino_schema.Message, accumulatedOutput string) {
	if span == nil {
		return
	}
	if accumulatedOutput != "" {
		span.SetAttributes(
			observability.GenAICompletion.String(accumulatedOutput),
			observability.LangfuseObservationOutput.String(accumulatedOutput),
		)
	} else if finalMsg != nil && finalMsg.Content != "" {
		usage := a.extractUsage(finalMsg)
		outputAttrs := a.buildOutputAttributes(finalMsg, usage)
		span.SetAttributes(outputAttrs...)
	}
	span.End()
}

func (a *EinoAgentAdapter) extractUsage(msg *eino_schema.Message) ChatUsage {
	model := a.getCurrentModelName(msg)
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return ChatUsage{Model: model}
	}
	promptTokens := int(msg.ResponseMeta.Usage.PromptTokens)
	completionTokens := int(msg.ResponseMeta.Usage.CompletionTokens)
	return ChatUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      int(msg.ResponseMeta.Usage.TotalTokens),
		Model:            model,
		Cost:             calculateLLMCost(model, promptTokens, completionTokens),
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
