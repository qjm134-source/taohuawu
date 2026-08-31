package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/watertown/guide/internal/config"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/pkg/logging"
)

// errorClass 模型调用错误的分类，决定熔断器对它的处理方式。
type errorClass uint8

const (
	// classExcluded 不计入熔断统计的错误（调用方主动取消），避免把用户行为归罪于模型。
	classExcluded errorClass = iota + 1 // 零值保留：未分类

	// classSoft 普通失败（5xx / 网络错误 / 超时 / 空响应）：进入滑动窗口计数，窗口内满 max_failures 次才熔断。
	classSoft

	// classHard 致命失败（配额耗尽 / 鉴权失败）：单次即熔断，并进入比 recovery_time 更长的冷却期。
	classHard
)

// hard 错误的冷却时长基于 recovery_time 的倍数：配额/鉴权问题不会在几分钟内自愈。
const (
	hardBaseMul = 6  // 首次 hard 熔断的冷却倍数（recovery_time × 6）
	hardMaxMul  = 64 // 指数退避封顶，防止冷却期无限增长
)

// classifyModelError 将底层模型调用错误分类。
//
// ADK 会把 provider 的 HTTP 错误包装成 NodeRunError，但其 Error() 字符串
// 保留了原始状态码与响应体（如 "status code: 403 ... Free quota exhausted"），
// 因此基于小写子串匹配分类是可靠的。匹配串集中在各 case，新增 provider
// 特有的错误码时在此追加。
func classifyModelError(err error) errorClass {
	if err == nil {
		return classExcluded // 成功不经过本函数，防御性返回
	}
	if errors.Is(err, context.Canceled) {
		// 调用方主动取消（如用户断开连接），不是模型的错。
		// 注意 DeadlineExceeded 不在此列：模型响应过慢导致的超时是真实的模型质量问题。
		return classExcluded
	}

	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg, "401", "unauthorized", "invalid api key", "invalid_api_key", "authentication", "apikey"):
		return classHard // 鉴权失败：key 无效/过期，重试无意义
	case containsAny(msg, "quota", "arrearage", "exhausted", "insufficient", "allocationquota", "free tier only", "billing"):
		return classHard // 配额耗尽/欠费（如百炼 AllocationQuota.FreeTierOnly）
	default:
		return classSoft // 5xx、网络错误、超时、空响应等
	}
}

// containsAny 依次检查 msg 是否包含任一子串。
func containsAny(msg string, subs ...string) bool {
	for _, s := range subs {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// modelCircuit 单个模型的熔断器：包装 failsafe-go 的 CircuitBreaker，补充两类能力——
//
//  1. 错误分类：failsafe 的状态机只认「失败/成功」，这里先用 classifyModelError 分类，
//     hard 类直接手动 Open（单次即熔断），soft 类走 RecordFailure 的窗口计数。
//  2. hard 冷却期：failsafe 的 open 恢复时间固定为 WithDelay 的基础值（standalone 用法下
//     DelayFunc 拿不到导致熔断的错误，无法按类别区分延迟），因此 hard 类的冷却由
//     hardUntil 时间戳在 wrapper 层实现，并按探测失败次数指数退避。
type modelCircuit struct {
	name         string                             // 模型 stats key（sanitize 后），用于日志与指标
	cb           circuitbreaker.CircuitBreaker[any] // 标准三态状态机
	baseRecovery time.Duration                      // open → half-open 的基础恢复时间（配置 recovery_time）
	hardUntil    atomic.Int64                       // hard 冷却截止时间（unix nano）；0 表示无冷却
	hardMul      atomic.Uint32                      // hard 冷却的当前退避倍数
}

// circuitSettings 熔断器运行参数。
// 从 config.CircuitConfig 解出为基本类型：config 的 duration 字段是私有类型，
// 包外（含测试）无法构造；本结构使熔断器逻辑可独立于 config 包实例化。
type circuitSettings struct {
	maxFailures   int
	failureWindow time.Duration
	recoveryTime  time.Duration
	halfOpenLimit int
}

// newModelCircuit 创建单模型熔断器。
//
//   - WithFailureThresholdPeriod：time-based 滑动窗口（内部 10 个时间片），窗口内失败数达到 maxFailures 开闸。
//   - WithDelay：open 状态的基础恢复时间。
//   - WithSuccessThreshold(halfOpenLimit)：half-open 需连续 halfOpenLimit 次探测成功才关闭，
//     任意一次探测失败立即重开；该值同时是半开状态的最大并发探测数（半开 permit 容量）。
func newModelCircuit(name string, s circuitSettings, logger logging.Logger) *modelCircuit {
	c := &modelCircuit{
		name:         name,
		baseRecovery: s.recoveryTime,
	}
	c.hardMul.Store(hardBaseMul)
	c.cb = circuitbreaker.NewBuilder[any]().
		WithFailureThresholdPeriod(uint(s.maxFailures), s.failureWindow).
		WithDelay(s.recoveryTime).
		WithSuccessThreshold(uint(s.halfOpenLimit)).
		OnStateChanged(func(e circuitbreaker.StateChangedEvent) {
			// failsafe 在状态迁移回调时会临时释放内部锁，此处可安全做 IO
			observability.LLMCircuitState.WithLabelValues(name).Set(float64(e.NewState))
			observability.LLMCircuitTransitions.WithLabelValues(name, e.OldState.String(), e.NewState.String()).Inc()
			logger.Warn("[Circuit] model state changed",
				"model", name, "from", e.OldState.String(), "to", e.NewState.String(),
				"remaining_delay", c.cb.RemainingDelay().String())
		}).
		Build()
	return c
}

// allow 判断当前是否允许向该模型发起请求。
// 半开探测由 failsafe 惰性完成：open 且恢复时间已过时，TryAcquirePermit 会自动转入半开并放行探测。
func (c *modelCircuit) allow() bool {
	if c.hardUntil.Load() > time.Now().UnixNano() {
		return false // hard 冷却期内直接拒绝，不碰 breaker（避免无谓消耗半开探测 permit）
	}
	return c.cb.TryAcquirePermit()
}

// available 仅查询是否可尝试，不消耗探测 permit（用于候选扫描）。
// open 且 RemainingDelay 已为 0 时，下一次 TryAcquirePermit 会转入半开放行，视为可用。
func (c *modelCircuit) available() bool {
	if c.hardUntil.Load() > time.Now().UnixNano() {
		return false
	}
	switch c.cb.State() {
	case circuitbreaker.ClosedState, circuitbreaker.HalfOpenState:
		return true
	default: // OpenState
		return c.cb.RemainingDelay() == 0
	}
}

// record 记录一次模型调用结果，err 为 nil 表示成功。
func (c *modelCircuit) record(err error) {
	class := classifyModelError(err)
	switch class {
	case classExcluded:
		// 用户取消不记录；成功时清除 hard 冷却与退避
		if err == nil {
			c.hardUntil.Store(0)
			c.hardMul.Store(hardBaseMul)
			c.cb.RecordSuccess()
		}
	case classSoft:
		c.cb.RecordFailure() // 窗口满时 failsafe 自动 open
	case classHard:
		// 单次即熔断：立即开闸 + 设置（带退避的）hard 冷却期
		mul := c.hardMul.Load()
		if mul < hardBaseMul {
			mul = hardBaseMul
		}
		c.hardUntil.Store(time.Now().Add(c.baseRecovery * time.Duration(mul)).UnixNano())
		// 指数退避：连续 hard 失败（含半开探测失败）时冷却期翻倍，封顶 hardMaxMul
		c.hardMul.Store(min(mul*2, hardMaxMul))
		if !c.cb.IsOpen() {
			c.cb.Open()
		}
	}
}

// circuitManager 管理全部模型的熔断器，按 stats key 惰性创建。
// 配置非法（任一关键字段 <= 0）时整体禁用：Allow 恒放行、Record 忽略，行为与接入前一致。
type circuitManager struct {
	mu       sync.Mutex
	breakers map[string]*modelCircuit
	s        circuitSettings
	enabled  bool
	logger   logging.Logger
}

func newCircuitManager(cfg config.CircuitConfig, logger logging.Logger) *circuitManager {
	return newCircuitManagerWithSettings(circuitSettings{
		maxFailures:   cfg.MaxFailures,
		failureWindow: cfg.FailureWindow.Duration,
		recoveryTime:  cfg.RecoveryTime.Duration,
		halfOpenLimit: cfg.HalfOpenLimit,
	}, logger)
}

func newCircuitManagerWithSettings(s circuitSettings, logger logging.Logger) *circuitManager {
	enabled := s.maxFailures > 0 && s.failureWindow > 0 && s.recoveryTime > 0
	if !enabled {
		logger.Info("[Circuit] disabled: invalid or missing circuit config",
			"max_failures", s.maxFailures,
			"failure_window", s.failureWindow.String(),
			"recovery_time", s.recoveryTime.String())
	}
	return &circuitManager{
		breakers: make(map[string]*modelCircuit),
		s:        s,
		enabled:  enabled,
		logger:   logger,
	}
}

// Allow 返回是否允许对指定模型发起请求。
func (m *circuitManager) Allow(key string) bool {
	if !m.enabled {
		return true
	}
	return m.getOrCreate(key).allow()
}

// Available 返回模型是否可尝试（无副作用的候选扫描）。
func (m *circuitManager) Available(key string) bool {
	if !m.enabled {
		return true
	}
	return m.getOrCreate(key).available()
}

// Record 记录一次模型调用结果。
func (m *circuitManager) Record(key string, err error) {
	if !m.enabled {
		return
	}
	m.getOrCreate(key).record(err)
}

// registerKeys 预注册已知模型 key，为其创建 closed 状态的熔断器。
// 使 AllUnavailable 能感知"存在但从未被调用过"的备用模型，避免备用模型
// 尚无记录时被误判为全部不可用。
func (m *circuitManager) registerKeys(keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		if _, ok := m.breakers[key]; !ok {
			m.breakers[key] = newModelCircuit(key, m.s, m.logger)
		}
	}
}

// AllUnavailable 判断是否所有模型都被熔断（用于请求入口快速失败，
// 省掉一次必然失败的 LLM 调用）。未启用熔断时恒为 false。
func (m *circuitManager) AllUnavailable() bool {
	if !m.enabled {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.breakers {
		if c.available() {
			return false
		}
	}
	return len(m.breakers) > 0
}

// getOrCreate 取指定模型的熔断器，不存在则创建（惰性初始化）。
func (m *circuitManager) getOrCreate(key string) *modelCircuit {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.breakers[key]
	if !ok {
		c = newModelCircuit(key, m.s, m.logger)
		m.breakers[key] = c
	}
	return c
}
