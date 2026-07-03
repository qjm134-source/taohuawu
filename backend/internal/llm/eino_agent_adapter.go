package llm

import (
	"context"
	"crypto/tls"
	"errors"
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
		Instruction:         "",
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

	handler := newEinoAgentHandler(a.logger)

	runOpts := []eino_adk.AgentRunOption{
		eino_adk.WithChatModelOptions(modelOpts),
		eino_adk.WithCallbacks(handler),
	}

	a.logger.Info("[Chat] Starting ADK agent run", "message_count", len(messages))
	startTime := time.Now()

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
		return nil, nil, lastErr
	}

	if finalMsg == nil {
		a.logger.Error("[Chat] No response from ADK agent", "latency", latency)
		return nil, nil, errors.New("no response from ADK agent")
	}

	usage = a.extractUsage(finalMsg)
	a.logger.Info("[Chat] ADK agent run success", "model", modelName, "latency", latency, "content_len", len(finalMsg.Content))

	return finalMsg, &usage, nil
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

	handler := newEinoAgentHandler(a.logger)

	runOpts := []eino_adk.AgentRunOption{
		eino_adk.WithChatModelOptions(modelOpts),
		eino_adk.WithCallbacks(handler),
	}

	a.logger.Info("[StreamChat] Starting ADK agent run", "message_count", len(messages))

	iter := a.runner.Run(ctx, messages, runOpts...)

	return &adkEventStream{
		adapter:   a,
		iter:      iter,
		ctx:       ctx,
		startTime: time.Now(),
	}, nil
}

type adkEventStream struct {
	adapter *EinoAgentAdapter
	iter    interface {
		Next() (*eino_adk.AgentEvent, bool)
	}
	ctx         context.Context
	msgStream   *eino_schema.StreamReader[*eino_schema.Message]
	modelName   string
	startTime   time.Time
	usage       *ChatUsage
	mu          sync.Mutex
	closed      bool
	eventBuffer []*StreamEvent
}

func (s *adkEventStream) Recv() (*StreamEvent, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, io.EOF
	}

	if len(s.eventBuffer) > 0 {
		event := s.eventBuffer[0]
		s.eventBuffer = s.eventBuffer[1:]
		s.mu.Unlock()
		return event, nil
	}

	if s.msgStream != nil {
		msgStream := s.msgStream
		s.mu.Unlock()
		return s.recvFromMsgStream(msgStream)
	}
	s.mu.Unlock()

	for {
		event, ok := s.iter.Next()
		if !ok {
			s.mu.Lock()
			s.closed = true
			s.recordStats(false)
			s.mu.Unlock()
			return &StreamEvent{
				Type:       StreamEventTypeAction,
				ActionType: "exit",
				Model:      s.modelName,
				Usage:      s.usage,
			}, nil
		}

		if event.Err != nil {
			s.adapter.logger.Error("[adkEventStream] Event error", "error", event.Err)
			continue
		}

		if event.Output != nil && event.Output.MessageOutput != nil {
			msgVariant := event.Output.MessageOutput

			if msgVariant.Role == eino_schema.Tool {
				continue
			}

			if msgVariant.IsStreaming && msgVariant.MessageStream != nil {
				s.mu.Lock()
				s.msgStream = msgVariant.MessageStream
				s.mu.Unlock()
				s.adapter.logger.Info("[adkEventStream] Got MessageStream")
				return s.recvFromMsgStream(msgVariant.MessageStream)
			}

			if m, err := msgVariant.GetMessage(); err == nil && m != nil {
				if len(m.ToolCalls) > 0 {
					continue
				}

				if m.Role == eino_schema.Assistant && m.Content != "" {
					s.mu.Lock()
					if s.modelName == "" {
						s.modelName = s.adapter.getCurrentModelName(m)
					}
					s.usage = &ChatUsage{}
					*s.usage = s.adapter.extractUsage(m)
					modelName := s.modelName
					usage := s.usage
					s.mu.Unlock()

					return &StreamEvent{
						Type:         StreamEventTypeChunk,
						Content:      m.Content,
						FinishReason: m.ResponseMeta.FinishReason,
						Model:        modelName,
						Usage:        usage,
					}, nil
				}
			}
		}
	}
}

func (s *adkEventStream) recvFromMsgStream(msgStream *eino_schema.StreamReader[*eino_schema.Message]) (*StreamEvent, error) {
	chunk, err := msgStream.Recv()
	if err == io.EOF {
		msgStream.Close()
		s.mu.Lock()
		s.msgStream = nil
		s.mu.Unlock()
		var finishReason string
		if chunk != nil && chunk.ResponseMeta != nil {
			finishReason = chunk.ResponseMeta.FinishReason
		}
		return &StreamEvent{
			Type:         StreamEventTypeChunk,
			Content:      "",
			FinishReason: finishReason,
			Model:        s.modelName,
			Usage:        s.usage,
		}, nil
	}
	if err != nil {
		msgStream.Close()
		s.mu.Lock()
		s.msgStream = nil
		s.mu.Unlock()
		return nil, err
	}

	s.mu.Lock()
	if s.modelName == "" {
		s.modelName = s.adapter.getCurrentModelName(chunk)
	}
	s.usage = &ChatUsage{}
	*s.usage = s.adapter.extractUsage(chunk)
	s.mu.Unlock()

	var finishReason string
	if chunk.ResponseMeta != nil {
		finishReason = chunk.ResponseMeta.FinishReason
	}

	return &StreamEvent{
		Type:         StreamEventTypeChunk,
		Content:      chunk.Content,
		FinishReason: finishReason,
		Model:        s.modelName,
		Usage:        s.usage,
	}, nil
}

func (s *adkEventStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	if s.msgStream != nil {
		s.msgStream.Close()
		s.msgStream = nil
	}
	s.recordStats(true)
	return nil
}

func (s *adkEventStream) recordStats(isError bool) {
	if s.startTime != (time.Time{}) && s.modelName != "" {
		latency := time.Since(s.startTime)
		s.adapter.recordStats(s.modelName, latency, isError, s.usage)
	}
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

func (a *EinoAgentAdapter) extractUsage(msg *eino_schema.Message) ChatUsage {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return ChatUsage{Model: a.getCurrentModelName(msg)}
	}
	return ChatUsage{
		PromptTokens:     int(msg.ResponseMeta.Usage.PromptTokens),
		CompletionTokens: int(msg.ResponseMeta.Usage.CompletionTokens),
		TotalTokens:      int(msg.ResponseMeta.Usage.TotalTokens),
		Model:            a.getCurrentModelName(msg),
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
