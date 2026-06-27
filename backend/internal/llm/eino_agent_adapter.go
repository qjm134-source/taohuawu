package llm

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"strings"
	"sync"
	"time"

	eino_openai "github.com/cloudwego/eino-ext/components/model/openai"
	eino_model "github.com/cloudwego/eino/components/model"
	eino_tool "github.com/cloudwego/eino/components/tool"
	eino_compose "github.com/cloudwego/eino/compose"
	eino_agent "github.com/cloudwego/eino/flow/agent"
	eino_react "github.com/cloudwego/eino/flow/agent/react"
	eino_schema "github.com/cloudwego/eino/schema"
	"github.com/watertown/guide/internal/config"
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

type modelEntry struct {
	name      string
	modelName string
	model     *eino_openai.ChatModel
	weight    float64
}

type modelStats struct {
	totalLatency time.Duration
	requestCount int
	errorCount   int
}

type EinoAgentAdapter struct {
	mu               sync.RWMutex
	agent            *eino_react.Agent
	models           []modelEntry
	fallback         []string
	strategy         Strategy
	fixedModel       string
	weights          map[string]float64
	logger           logging.Logger
	stats            map[string]*modelStats
	capabilityMap    map[string][]string
	timeout          time.Duration
	primaryModelName string // 主模型的配置名称，用于日志和指标
}

func NewEinoAgentAdapter(logger logging.Logger, cfg config.LLMConfig, tools []eino_tool.InvokableTool) *EinoAgentAdapter {
	logger.Info("[NewEinoAgentAdapter] Creating adapter", "tool_count", len(tools))
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
	}

	for _, mc := range cfg.Models {
		if !mc.Enabled {
			continue
		}

		chatModel, err := eino_openai.NewChatModel(context.Background(), &eino_openai.ChatModelConfig{
			Model:   mc.Name,
			APIKey:  mc.APIKey,
			BaseURL: mc.BaseURL,
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
		adapter.agent = adapter.buildEinoAgent(primaryModel.model, tools)
	}

	return adapter
}

func (a *EinoAgentAdapter) buildEinoAgent(primaryModel *eino_openai.ChatModel, tools []eino_tool.InvokableTool) *eino_react.Agent {
	config := &eino_react.AgentConfig{
		ToolCallingModel: primaryModel,
		GraphName:        "WaterTownReActAgent",
	}

	if len(tools) > 0 {
		a.logger.Info("[buildEinoAgent] Registering tools for ReAct agent", "tool_count", len(tools))
		einoToolList := make([]eino_tool.BaseTool, 0, len(tools))
		for _, t := range tools {
			info, _ := t.Info(context.Background())
			a.logger.Info("[buildEinoAgent] Adding tool", "name", info.Name)
			einoToolList = append(einoToolList, t)
		}
		config.ToolsConfig = eino_compose.ToolsNodeConfig{
			Tools: einoToolList,
		}
	}

	agent, err := eino_react.NewAgent(context.Background(), config)
	if err != nil {
		a.logger.Error("Failed to create ReAct agent", "error", err)
		return nil
	}

	a.logger.Info("[buildEinoAgent] ReAct agent created successfully", "tool_count", len(tools))
	return agent
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
	return a.agent != nil
}

func (a *EinoAgentAdapter) Chat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (*eino_schema.Message, *ChatUsage, error) {
	if a.agent == nil {
		return nil, nil, errors.New("no ReAct agent available")
	}

	chatOpts := &ChatOptions{}
	for _, o := range opts {
		o(chatOpts)
	}

	modelOpts := []eino_model.Option{
		eino_model.WithTemperature(chatOpts.Temperature),
		eino_model.WithMaxTokens(chatOpts.MaxTokens),
	}

	// 创建回调处理器用于 trace 和审计日志
	handler := newEinoAgentHandler(a.logger)

	agentOpts := []eino_agent.AgentOption{
		eino_react.WithChatModelOptions(modelOpts...),
		eino_agent.WithComposeOptions(
			eino_compose.WithCallbacks(handler),
		),
	}

	a.logger.Info("[Chat] Starting ReAct agent generate", "message_count", len(messages))
	startTime := time.Now()
	msg, err := a.agent.Generate(ctx, messages, agentOpts...)

	latency := time.Since(startTime)
	modelName := a.getCurrentModelName(msg)
	a.recordStats(modelName, latency, msg == nil || msg.Content == "")

	if err != nil {
		a.logger.Error("[Chat] ReAct agent generate failed", "error", err, "latency", latency)
		return nil, nil, err
	}

	if msg == nil {
		a.logger.Error("[Chat] No response from ReAct agent", "latency", latency)
		return nil, nil, errors.New("no response from ReAct agent")
	}

	usage := a.extractUsage(msg)
	a.logger.Info("[Chat] ReAct agent generate success", "model", modelName, "latency", latency, "content_len", len(msg.Content))

	return msg, &usage, nil
}

func (a *EinoAgentAdapter) StreamChat(ctx context.Context, messages []*eino_schema.Message, opts ...ChatOption) (Stream, error) {
	if a.agent == nil {
		return nil, errors.New("no ReAct agent available")
	}

	chatOpts := &ChatOptions{}
	for _, o := range opts {
		o(chatOpts)
	}

	modelOpts := []eino_model.Option{
		eino_model.WithTemperature(chatOpts.Temperature),
		eino_model.WithMaxTokens(chatOpts.MaxTokens),
	}

	// 创建回调处理器用于 trace 和审计日志
	handler := newEinoAgentHandler(a.logger)

	agentOpts := []eino_agent.AgentOption{
		eino_react.WithChatModelOptions(modelOpts...),
		eino_agent.WithComposeOptions(
			eino_compose.WithCallbacks(handler),
		),
	}

	a.logger.Info("[StreamChat] Starting ReAct agent stream", "message_count", len(messages))
	stream, err := a.agent.Stream(ctx, messages, agentOpts...)
	if err != nil {
		a.logger.Error("[StreamChat] ReAct agent stream failed", "error", err)
		return nil, err
	}

	return &reactStream{
		adapter:   a,
		stream:    stream,
		ctx:       ctx,
		startTime: time.Now(),
	}, nil
}

type reactStream struct {
	adapter    *EinoAgentAdapter
	stream     *eino_schema.StreamReader[*eino_schema.Message]
	ctx        context.Context
	startTime  time.Time
	modelName  string
	hasContent bool
}

func (s *reactStream) Recv() (*StreamChunk, error) {
	msg, err := s.stream.Recv()
	if errors.Is(err, io.EOF) {
		s.recordStats()
		s.adapter.logger.Info("[ReactStream] Stream closed", "model", s.modelName, "has_content", s.hasContent)
		return nil, io.EOF
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.adapter.logger.Error("[ReactStream] Recv error", "error", err)
		}
		return nil, err
	}
	if msg == nil {
		return nil, nil
	}

	if s.modelName == "" {
		s.modelName = s.adapter.getCurrentModelName(msg)
	}

	if msg.Content != "" {
		s.hasContent = true
	}

	return &StreamChunk{
		Content:      msg.Content,
		FinishReason: msg.ResponseMeta.FinishReason,
		Model:        s.modelName,
		Usage:        s.adapter.extractUsage(msg),
	}, nil
}

func (s *reactStream) Close() error {
	s.stream.Close()
	s.recordStats()
	return nil
}

func (s *reactStream) recordStats() {
	if s.hasContent {
		latency := time.Since(s.startTime)
		s.adapter.recordStats(s.modelName, latency, false)
	}
}

func (a *EinoAgentAdapter) getCurrentModelName(msg *eino_schema.Message) string {
	// 优先从 eino 消息 Extra 中获取
	if msg != nil && msg.ResponseMeta != nil {
		if name, ok := msg.Extra["model_name"].(string); ok && name != "" {
			return name
		}
	}
	// 回退到配置中的主模型名称
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

func (a *EinoAgentAdapter) recordStats(modelName string, latency time.Duration, isError bool) {
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
