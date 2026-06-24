package router

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/watertown/guide/internal/llm/model"
	"github.com/watertown/guide/pkg/logging"
)

// withoutCancel 返回不携带 deadline/cancel 的 ctx（保留 values）。
// Go 1.21+ 内置了 context.WithoutCancel，这里直接使用它。
var withoutCancel = context.WithoutCancel

// Strategy 定义路由策略类型。
type Strategy string

const (
	StrategyFixed      Strategy = "fixed"      // 固定使用指定模型
	StrategyCost       Strategy = "cost"       // 优先选择价格最低的模型
	StrategyLatency    Strategy = "latency"    // 优先选择延迟最低的模型
	StrategyCapability Strategy = "capability" // 根据任务类型选择合适的模型
	StrategyFallback   Strategy = "fallback"   // 使用降级链
	StrategyWeighted   Strategy = "weighted"   // 按权重随机选择
)

// Router 统一的模型路由器，根据策略选择合适的 Provider。
type Router struct {
	mu sync.RWMutex

	// providers 是所有可用的 provider，按名称索引。
	providers map[string]*providerWrapper

	// strategy 是当前使用的路由策略。
	strategy Strategy

	// fixedModel 在 Fixed 策略下指定固定使用的模型。
	fixedModel string

	// weights 在 Weighted 策略下指定各模型的权重。
	weights map[string]float64

	// fallbackChain 是降级链，从主模型到兜底模型。
	fallbackChain []string

	// capabilityMap 是能力映射，指定哪些模型适合什么任务。
	capabilityMap map[model.TaskType][]string

	// timeout 是每个 provider 请求的超时时间
	timeout time.Duration

	// logger 用于记录日志
	logger logging.Logger
}

// providerWrapper 包装 provider 及其统计数据。
type providerWrapper struct {
	provider model.Provider
	stats    *model.ModelStats
	enabled  bool
}

// NewRouter 创建一个新的路由器。
func NewRouter(logger logging.Logger, timeout time.Duration) *Router {
	return &Router{
		providers:     make(map[string]*providerWrapper),
		strategy:      StrategyFallback, // 默认使用降级策略
		weights:       make(map[string]float64),
		fallbackChain: make([]string, 0),
		capabilityMap: make(map[model.TaskType][]string),
		timeout:       timeout,
		logger:        logger,
	}
}

// RegisterProvider 注册一个 provider。
func (r *Router) RegisterProvider(provider model.Provider, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := provider.Name()
	wrapper := &providerWrapper{
		provider: provider,
		stats:    model.NewModelStats(),
		enabled:  enabled,
	}

	if existing, ok := r.providers[name]; ok {
		// 保持现有的统计数据
		wrapper.stats = existing.stats
	}

	r.providers[name] = wrapper
}

// SetStrategy 设置路由策略。
func (r *Router) SetStrategy(strategy Strategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.strategy = strategy
}

// SetFixedModel 设置 Fixed 策略下使用的固定模型。
func (r *Router) SetFixedModel(model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fixedModel = model
}

// SetWeight 设置 Weighted 策略下指定模型的权重。
func (r *Router) SetWeight(providerName string, weight float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.weights[providerName] = weight
}

// SetFallbackChain 设置降级链。
// 链中越靠前的模型优先级越高。
func (r *Router) SetFallbackChain(providerNames []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallbackChain = append([]string{}, providerNames...)
}

// SetCapabilityMap 设置能力映射。
func (r *Router) SetCapabilityMap(taskType model.TaskType, providerNames []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capabilityMap[taskType] = append([]string{}, providerNames...)
}

// RouteRequest 根据当前策略选择 provider 并执行请求。
// 注意：provider.Chat() 可能耗时较长，必须在锁外执行，否则会阻塞所有后续请求。
func (r *Router) RouteRequest(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	// 第一步：在锁内选择 provider
	r.mu.RLock()
	provider, err := r.selectProvider(req)
	r.mu.RUnlock()
	if err != nil {
		r.logger.Error("[Router] Failed to select provider", "error", err)
		return nil, err
	}

	// 记录选中的 provider
	r.logger.Info("[Router] Selected provider", "name", provider.Name(), "model", req.Model)

	// 第二步：在锁外执行 LLM 调用（可能耗时很长）
	startTime := nowTime()
	resp, err := provider.Chat(ctx, req)
	latency := nowTime().Sub(startTime)

	// 第三步：记录统计信息（锁内）
	r.recordStats(provider.Name(), latency, err != nil)

	if err != nil {
		r.logger.Error("[Router] Provider chat failed",
			"provider", provider.Name(),
			"latency", latency,
			"error", err)

		// 第四步：主 provider 失败，尝试降级链中的下一个
		// 降级链容忍更高错误率，确保可用性优先于成本
		if r.hasFallback() {
			r.logger.Info("[Router] Trying fallback provider")
			return r.tryFallback(ctx, req, provider.Name())
		}
		return nil, err
	}

	r.logger.Info("[Router] Provider chat succeeded",
		"provider", provider.Name(),
		"latency", latency)

	// 如果响应中的模型名称为空，使用 provider 的名称作为模型名称
	if resp.Model == "" {
		resp.Model = provider.Name()
		r.logger.Debug("[Router] Model name was empty, set to", "model", resp.Model)
	}

	// 检查响应是否为空内容，如果是空的并且有降级链，则尝试降级
	if isEmptyResponse(resp) && r.hasFallback() {
		r.logger.Warn("[Router] Provider returned empty response, trying fallback",
			"provider", provider.Name(),
			"choices", len(resp.Choices),
			"model", resp.Model)
		return r.tryFallback(ctx, req, provider.Name())
	}

	return resp, nil
}

// RouteRequestStream 根据当前策略选择 provider 并执行流式请求。
func (r *Router) RouteRequestStream(ctx context.Context, req *model.ChatRequest) (<-chan model.StreamChunk, error) {
	// 第一步：在锁内选择 provider
	r.mu.RLock()
	provider, err := r.selectProvider(req)
	r.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	r.logger.Info("[Router] Starting stream chat", "provider", provider.Name())

	// 第二步：在锁外执行流式请求
	startTime := nowTime()
	stream, err := provider.StreamChat(ctx, req)

	if err != nil {
		r.logger.Error("[Router] Stream chat failed", "provider", provider.Name(), "error", err)
		if r.hasFallback() {
			return r.tryFallbackStream(ctx, req, provider.Name())
		}
		return nil, err
	}

	// 使用 tee 模式复制 stream：一个用于统计，一个用于返回
	out := make(chan model.StreamChunk)
	go func() {
		defer close(out)
		var firstChunkTime time.Time
		var hasError bool
		chunkCount := 0

		for chunk := range stream {
			chunkCount++
			r.logger.Info("[Router] Stream chunk received", "count", chunkCount, "content_len", len(chunk.Content), "model", chunk.Model, "done", chunk.Done)

			// 记录统计信息
			if !chunk.Done && !hasError {
				if chunk.Index == 0 {
					firstChunkTime = nowTime()
				}
				if chunk.Err != nil {
					hasError = true
				}
			}

			// 发送到输出 channel
			out <- chunk
		}

		r.logger.Info("[Router] Stream completed", "total_chunks", chunkCount, "has_error", hasError)

		// 记录统计信息
		if !firstChunkTime.IsZero() {
			latency := firstChunkTime.Sub(startTime)
			r.recordStats(provider.Name(), latency, hasError)
		}
	}()

	return out, nil
}

// SelectModel 根据当前策略选择模型，但不执行请求。
// 返回选中的模型名称，用于后续的模式判断。
func (r *Router) SelectModel(req *model.ChatRequest) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, err := r.selectProvider(req)
	if err != nil {
		return "", err
	}

	return provider.Name(), nil
}

// RouteRequestWithModel 使用指定的模型执行非流式请求。
func (r *Router) RouteRequestWithModel(ctx context.Context, req *model.ChatRequest, modelName string) (*model.ChatResponse, error) {
	r.mu.RLock()
	provider, err := r.getProvider(modelName)
	r.mu.RUnlock()
	if err != nil {
		r.logger.Error("[Router] Failed to get provider", "model", modelName, "error", err)
		return nil, err
	}

	r.logger.Info("[Router] Executing chat with specified model", "model", modelName)

	startTime := nowTime()
	resp, err := provider.Chat(ctx, req)
	latency := nowTime().Sub(startTime)

	r.recordStats(modelName, latency, err != nil)

	if err != nil {
		r.logger.Error("[Router] Chat failed", "model", modelName, "error", err)
		return nil, err
	}

	r.logger.Info("[Router] Chat succeeded", "model", modelName, "latency", latency)

	// 如果响应中的模型名称为空，使用 provider 的名称作为模型名称
	if resp.Model == "" {
		resp.Model = modelName
	}

	return resp, nil
}

// RouteRequestStreamWithModel 使用指定的模型执行流式请求。
func (r *Router) RouteRequestStreamWithModel(ctx context.Context, req *model.ChatRequest, modelName string) (<-chan model.StreamChunk, error) {
	r.mu.RLock()
	provider, err := r.getProvider(modelName)
	r.mu.RUnlock()
	if err != nil {
		r.logger.Error("[Router] Failed to get provider for stream", "model", modelName, "error", err)
		return nil, err
	}

	r.logger.Info("[Router] Starting stream chat with specified model", "model", modelName)

	startTime := nowTime()
	stream, err := provider.StreamChat(ctx, req)

	if err != nil {
		r.logger.Error("[Router] Stream chat failed", "model", modelName, "error", err)
		r.recordStats(modelName, 0, true)
		return nil, err
	}

	// 使用 tee 模式复制 stream：一个用于统计，一个用于返回
	out := make(chan model.StreamChunk, 100)
	go func() {
		defer close(out)
		var firstChunkTime time.Time
		var hasError bool
		chunkCount := 0

		for chunk := range stream {
			chunkCount++
			r.logger.Info("[Router] Stream chunk received", "model", modelName, "count", chunkCount, "content_len", len(chunk.Content))

			// 记录统计信息
			if !chunk.Done && !hasError {
				if chunk.Index == 0 {
					firstChunkTime = nowTime()
				}
				if chunk.Err != nil {
					hasError = true
				}
			}

			// 发送到输出 channel
			out <- chunk
		}

		r.logger.Info("[Router] Stream completed", "model", modelName, "total_chunks", chunkCount, "has_error", hasError)

		// 记录统计信息
		if !firstChunkTime.IsZero() {
			latency := firstChunkTime.Sub(startTime)
			r.recordStats(modelName, latency, hasError)
		}
	}()

	return out, nil
}

// selectProvider 根据当前策略选择 provider。
func (r *Router) selectProvider(req *model.ChatRequest) (model.Provider, error) {
	switch r.strategy {
	case StrategyFixed:
		return r.selectFixed()
	case StrategyCost:
		return r.selectByCost(req)
	case StrategyLatency:
		return r.selectByLatency(req)
	case StrategyCapability:
		return r.selectByCapability(req)
	case StrategyWeighted:
		return r.selectWeighted()
	case StrategyFallback:
		return r.selectFallback()
	default:
		return r.selectFallback()
	}
}

// selectFixed 选择固定模型。
func (r *Router) selectFixed() (model.Provider, error) {
	if r.fixedModel == "" {
		return nil, fmt.Errorf("fixed model not specified")
	}
	return r.getProvider(r.fixedModel)
}

// selectByCost 选择成本最低的 provider。
// 成本 = (输入 tokens * 输入价格 + 输出 tokens * 输出价格)
// 由于输出 tokens 未知，使用 1:1 比例估算。
func (r *Router) selectByCost(req *model.ChatRequest) (model.Provider, error) {
	candidates := r.getEnabledProviders()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled providers")
	}

	// 估算输入 tokens
	inputTokens := model.EstimateTokens(r.messagesToString(req.Messages))

	// 按成本排序
	sort.Slice(candidates, func(i, j int) bool {
		costI := r.calculateCost(candidates[i], inputTokens)
		costJ := r.calculateCost(candidates[j], inputTokens)
		return costI < costJ
	})

	return candidates[0].provider, nil
}

// selectByLatency 选择延迟最低的 provider。
func (r *Router) selectByLatency(req *model.ChatRequest) (model.Provider, error) {
	candidates := r.getEnabledProviders()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled providers")
	}

	// 按延迟排序（使用 EMA 延迟）
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].stats.Score() < candidates[j].stats.Score()
	})

	return candidates[0].provider, nil
}

// selectByCapability 根据任务类型选择合适的 provider。
func (r *Router) selectByCapability(req *model.ChatRequest) (model.Provider, error) {
	taskType := model.ClassifyTask(r.messagesToString(req.Messages))

	r.logger.Info("[Router] task type(selectByCapability): %s", taskType)

	// 获取适合该任务的 provider 列表
	providers, ok := r.capabilityMap[taskType]
	if !ok || len(providers) == 0 {
		// 没有专门配置的 provider，使用所有启用的 provider
		candidates := r.getEnabledProviders()
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no enabled providers")
		}
		return candidates[0].provider, nil
	}

	// 从适合的 provider 中选择第一个可用的
	for _, name := range providers {
		if provider, err := r.getProvider(name); err == nil {
			return provider, nil
		}
	}

	return nil, fmt.Errorf("no available providers for task type: %s", taskType)
}

// selectWeighted 按权重随机选择 provider。
func (r *Router) selectWeighted() (model.Provider, error) {
	candidates := r.getEnabledProviders()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled providers")
	}

	// 计算总权重
	totalWeight := 0.0
	for _, c := range candidates {
		weight := r.weights[c.provider.Name()]
		if weight <= 0 {
			weight = 1.0 // 默认权重
		}
		totalWeight += weight
	}

	// 随机选择
	threshold := randFloat() * totalWeight
	accumulated := 0.0

	for _, c := range candidates {
		weight := r.weights[c.provider.Name()]
		if weight <= 0 {
			weight = 1.0
		}
		accumulated += weight
		if accumulated >= threshold {
			return c.provider, nil
		}
	}

	return candidates[0].provider, nil
}

// selectFallback 选择降级链中的第一个可用 provider。
func (r *Router) selectFallback() (model.Provider, error) {
	for _, name := range r.fallbackChain {
		if provider, err := r.getProvider(name); err == nil {
			return provider, nil
		}
	}

	// 降级链为空或全部失败，使用第一个可用的 provider
	candidates := r.getEnabledProviders()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled providers")
	}
	return candidates[0].provider, nil
}

// tryFallback 尝试使用降级链中的下一个 provider，跳过已失败的 skipProvider。
// 每个降级 provider 使用独立的 context（剥离上游 deadline），
// 避免主 provider 耗时过长导致降级 provider 没有足够时间。
func (r *Router) tryFallback(ctx context.Context, req *model.ChatRequest, skipProvider string) (*model.ChatResponse, error) {
	// 降级链容忍更高错误率，确保可用性优先于成本
	for _, name := range r.fallbackChain {
		if name == skipProvider {
			continue // 跳过已经失败的 provider
		}
		if provider, err := r.getProvider(name); err == nil {
			r.logger.Info("[Router] Trying fallback provider", "name", name)
			startTime := nowTime()

			// 为每个降级 provider 创建独立的带超时 context
			// 剥离上游 deadline，避免主 provider 超时导致降级请求也立即失败
			fallbackCtx, cancel := context.WithTimeout(withoutCancel(ctx), r.timeout)
			resp, err := provider.Chat(fallbackCtx, req)
			cancel()

			latency := nowTime().Sub(startTime)
			r.recordStats(name, latency, err != nil)
			if err == nil {
				r.logger.Info("[Router] Fallback provider succeeded", "name", name, "latency", latency)
				return resp, nil
			}
			r.logger.Error("[Router] Fallback provider failed", "name", name, "error", err)
		}
	}
	r.logger.Error("[Router] All providers in fallback chain failed", "chain", r.fallbackChain)
	return nil, fmt.Errorf("all providers in fallback chain failed")
}

// tryFallbackStream 尝试使用降级链中的下一个 provider 进行流式请求，跳过已失败的 skipProvider。
// 每个降级 provider 使用独立的 context（剥离上游 deadline），
// 避免主 provider 耗时过长导致降级 provider 没有足够时间。
func (r *Router) tryFallbackStream(ctx context.Context, req *model.ChatRequest, skipProvider string) (<-chan model.StreamChunk, error) {
	for _, name := range r.fallbackChain {
		if name == skipProvider {
			continue
		}
		if provider, err := r.getProvider(name); err == nil {
			startTime := nowTime()

			// 为每个降级 provider 创建独立的带超时 context
			fallbackCtx, cancel := context.WithTimeout(withoutCancel(ctx), r.timeout)
			stream, err := provider.StreamChat(fallbackCtx, req)
			if err == nil {
				// 启动 goroutine 记录统计信息（简化版）
				go func() {
					<-stream
					cancel()
					latency := nowTime().Sub(startTime)
					r.recordStats(name, latency, false)
				}()
				return stream, nil
			}
			cancel()
		}
	}
	return nil, fmt.Errorf("all providers in fallback chain failed")
}

// recordStats 记录 provider 的统计信息。
func (r *Router) recordStats(providerName string, latency time.Duration, hasError bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if wrapper, ok := r.providers[providerName]; ok {
		wrapper.stats.RecordLatency(latency)
		wrapper.stats.RecordError(hasError)
	}
}

// getProvider 获取指定名称的 provider。
func (r *Router) getProvider(name string) (model.Provider, error) {
	wrapper, ok := r.providers[name]
	if !ok || !wrapper.enabled {
		return nil, fmt.Errorf("provider not found or disabled: %s", name)
	}
	return wrapper.provider, nil
}

// getEnabledProviders 获取所有启用的 provider。
func (r *Router) getEnabledProviders() []*providerWrapper {
	providers := make([]*providerWrapper, 0)
	for _, wrapper := range r.providers {
		if wrapper.enabled {
			providers = append(providers, wrapper)
		}
	}
	return providers
}

// hasFallback 检查是否配置了降级链。
func (r *Router) hasFallback() bool {
	return len(r.fallbackChain) > 0
}

// calculateCost 计算指定 provider 的成本。
func (r *Router) calculateCost(wrapper *providerWrapper, inputTokens int) float64 {
	// 假设输出 tokens 与输入 tokens 相同
	outputTokens := inputTokens
	cost := float64(inputTokens)/1000*wrapper.provider.InputPricePer1K() +
		float64(outputTokens)/1000*wrapper.provider.OutputPricePer1K()
	return cost
}

// messagesToString 将消息列表转换为字符串，用于任务分类。
func (r *Router) messagesToString(msgs []model.Message) string {
	var sb strings.Builder
	for _, msg := range msgs {
		sb.WriteString(string(msg.Role))
		sb.WriteString(": ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// isEmptyResponse 检查响应是否为空内容（有 tool_calls 不算空）。
func isEmptyResponse(resp *model.ChatResponse) bool {
	if resp == nil {
		return true
	}
	if len(resp.Choices) == 0 {
		return true
	}
	for _, choice := range resp.Choices {
		if choice.Message.Content != "" || len(choice.Message.ToolCalls) > 0 {
			return false
		}
	}
	return true
}

// nowTime 获取当前时间，便于测试时注入 mock。
var nowTime = func() time.Time {
	return time.Now()
}

// randFloat 生成随机浮点数，便于测试时注入 mock。
var randFloat = func() float64 {
	return rand.Float64()
}

// HasEnabledProvider 检查是否有已启用的 provider。
func (r *Router) HasEnabledProvider() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, wrapper := range r.providers {
		if wrapper.enabled {
			return true
		}
	}
	return false
}

// GetRegisteredProviders 返回所有已注册的 provider 名称。
func (r *Router) GetRegisteredProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
