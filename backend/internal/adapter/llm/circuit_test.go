package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/sirupsen/logrus"
	"github.com/watertown/guide/pkg/logging"
)

// testCircuitSettings 返回测试用熔断参数：窗口内 3 次失败开闸，20ms 后进入半开探测。
func testCircuitSettings() circuitSettings {
	return circuitSettings{
		maxFailures:   3,
		failureWindow: 10 * time.Second,
		recoveryTime:  20 * time.Millisecond,
		halfOpenLimit: 1,
	}
}

// nopLogger 测试用空日志实现，吞掉全部输出。
type nopLogger struct{}

// 编译期确保 nopLogger 满足 logging.Logger 接口
var _ logging.Logger = nopLogger{}

func (nopLogger) Debug(...interface{})          {}
func (nopLogger) Debugf(string, ...interface{}) {}
func (nopLogger) Info(...interface{})           {}
func (nopLogger) Infof(string, ...interface{})  {}
func (nopLogger) Warn(...interface{})           {}
func (nopLogger) Warnf(string, ...interface{})  {}
func (nopLogger) Error(...interface{})          {}
func (nopLogger) Errorf(string, ...interface{}) {}
func (nopLogger) Fatal(...interface{})          {}
func (nopLogger) Fatalf(string, ...interface{}) {}
func (nopLogger) WithFields(logrus.Fields) *logrus.Entry {
	return logrus.NewEntry(logrus.New())
}

// newTestManager 返回启用状态的测试管理器。
func newTestManager(t *testing.T) *circuitManager {
	t.Helper()
	return newCircuitManagerWithSettings(testCircuitSettings(), nopLogger{})
}

func TestClassifyModelError(t *testing.T) {
	// quotaErr 来自真实日志：百炼免费额度耗尽时的 provider 响应串
	quotaErr := errors.New(`error, status code: 403, status: 403 Forbidden, message: Free quota exhausted. ` +
		`To continue accessing the model on a paid basis, please add funds or disable the "use free tier only" mode ` +
		`in the management console., code: AllocationQuota.FreeTierOnly`)

	tests := []struct {
		name string
		err  error
		want errorClass
	}{
		{"nil error", nil, classExcluded},
		{"context canceled", context.Canceled, classExcluded},
		{"real bailian quota error", quotaErr, classHard},
		{"quota keyword", errors.New("400 AllocationQuota.FreeTierOnly: quota exhausted"), classHard},
		{"arrearage", errors.New("Arrearage: account in arrears"), classHard},
		{"unauthorized", errors.New("401 Unauthorized"), classHard},
		{"invalid api key", errors.New("Error: invalid api key"), classHard},
		{"server error", errors.New("500 Internal Server Error"), classSoft},
		{"timeout", errors.New("ResponseHeaderTimeout: waiting for response headers"), classSoft},
		{"connection refused", errors.New("dial tcp: connection refused"), classSoft},
		{"rate limit counts soft", errors.New("429 Too Many Requests"), classSoft},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyModelError(tt.err); got != tt.want {
				t.Errorf("classifyModelError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// 验证 ctx.DeadlineExceeded 被视为模型失败而非用户取消（模型响应过慢是真实的质量信号）。
func TestClassifyModelErrorDeadlineCounts(t *testing.T) {
	if got := classifyModelError(context.DeadlineExceeded); got != classSoft {
		t.Errorf("classifyModelError(DeadlineExceeded) = %v, want classSoft", got)
	}
}

// 窗口内 soft 失败达到阈值后熔断开闸；恢复时间过后进入半开放行探测；探测成功关闭。
func TestCircuitSoftFailuresOpenAndRecover(t *testing.T) {
	m := newTestManager(t)
	const key = "m1"

	// 1 次 permit（closed 恒放行）+ 3 次失败达到 MaxFailures → open
	m.Record(key, errors.New("500 internal"))
	m.Record(key, errors.New("500 internal"))
	m.Record(key, errors.New("500 internal"))
	if m.Allow(key) {
		t.Fatal("circuit should be open after 3 soft failures")
	}

	// 等待恢复时间（20ms）后进入半开，放行一个探测请求
	time.Sleep(40 * time.Millisecond)
	if !m.Allow(key) {
		t.Fatal("circuit should allow probe request after recovery time")
	}
	// 半开状态下探测 permit 已耗尽，其余请求应被拒绝
	if m.Allow(key) {
		t.Fatal("half-open should reject requests beyond probe limit")
	}

	// 探测成功 → 关闭
	m.Record(key, nil)
	if !m.Allow(key) {
		t.Fatal("circuit should be closed after successful probe")
	}
}

// 半开探测失败会重新开闸。
func TestCircuitProbeFailureReopens(t *testing.T) {
	m := newTestManager(t)
	const key = "m1"

	for i := 0; i < 3; i++ {
		m.Record(key, errors.New("500 internal"))
	}
	time.Sleep(40 * time.Millisecond)

	if !m.Allow(key) {
		t.Fatal("should allow probe after recovery")
	}
	m.Record(key, errors.New("500 internal")) // 探测失败 → 重开
	if m.Allow(key) {
		t.Fatal("circuit should reopen after failed probe")
	}
}

// hard 错误（配额/鉴权）单次即熔断，且冷却期为 recovery_time × hardBaseMul。
func TestCircuitHardErrorTripsImmediately(t *testing.T) {
	m := newTestManager(t)
	const key = "m1"

	m.Record(key, errors.New("403 AllocationQuota.FreeTierOnly"))
	if m.Allow(key) {
		t.Fatal("single hard error should trip the circuit immediately")
	}

	// 基础恢复时间（20ms）过后仍在 hard 冷却期（×6 = 120ms），不允许请求
	time.Sleep(40 * time.Millisecond)
	if m.Allow(key) {
		t.Fatal("hard cooldown (6x recovery) should still block after base recovery time")
	}

	// 冷却期结束后放行探测
	time.Sleep(120 * time.Millisecond)
	if !m.Allow(key) {
		t.Fatal("hard cooldown should expire and allow probe")
	}

	// 探测成功 → 关闭且退避倍数重置
	m.Record(key, nil)
	if !m.Allow(key) {
		t.Fatal("should close after successful probe")
	}
}

// hard 冷却期内探测再次失败（配额仍未恢复）时，冷却期按指数退避翻倍。
func TestCircuitHardBackoffDoubles(t *testing.T) {
	m := newTestManager(t)
	const key = "m1"

	m.Record(key, errors.New("403 quota exhausted"))
	time.Sleep(140 * time.Millisecond) // 第一次冷却 20ms×6=120ms

	if !m.Allow(key) {
		t.Fatal("first hard cooldown should have expired")
	}
	m.Record(key, errors.New("403 quota exhausted")) // 探测失败：hard 重trip，倍数 6→12

	// 第二次冷却 = 20ms×12 = 240ms；在 120ms 处（旧冷却期长度）仍应被拒绝
	time.Sleep(40 * time.Millisecond)
	if m.Allow(key) {
		t.Fatal("backed-off cooldown should still block after old cooldown length")
	}
	time.Sleep(220 * time.Millisecond) // 共 260ms > 240ms
	if !m.Allow(key) {
		t.Fatal("backed-off cooldown should expire eventually")
	}
}

// 成功记录会清除 hard 冷却与退避倍数。
func TestCircuitSuccessResetsHardState(t *testing.T) {
	m := newTestManager(t)
	const key = "m1"

	m.Record(key, errors.New("401 unauthorized"))
	m.Record(key, nil) // 成功（例如探测恰逢 provider 恢复）

	c := m.getOrCreate(key)
	if c.hardUntil.Load() != 0 {
		t.Fatal("success should clear hard cooldown")
	}
	if got := c.hardMul.Load(); got != hardBaseMul {
		t.Errorf("hardMul = %d, want reset to %d", got, hardBaseMul)
	}
}

// 用户取消不计入熔断统计：MaxFailures 次取消不会开闸。
func TestCircuitIgnoresContextCancellation(t *testing.T) {
	m := newTestManager(t)
	const key = "m1"

	for i := 0; i < 5; i++ {
		m.Record(key, context.Canceled)
	}
	if !m.Allow(key) {
		t.Fatal("context cancellations must not trip the circuit")
	}
}

// AllUnavailable：部分模型可用时为 false，全部不可用时为 true。
// m2 注册后从未被调用，应视为可用（对应生产中备用模型尚无调用记录的场景）。
func TestAllUnavailable(t *testing.T) {
	m := newTestManager(t)
	m.registerKeys("m1", "m2")

	m.Record("m1", errors.New("403 quota exhausted"))
	if m.AllUnavailable() {
		t.Fatal("m2 is still available, should not be all unavailable")
	}

	m.Record("m2", errors.New("403 quota exhausted"))
	if !m.AllUnavailable() {
		t.Fatal("all models tripped, should be all unavailable")
	}
}

// 未启用（配置非法）时熔断器为纯透传。
func TestDisabledManagerPassesThrough(t *testing.T) {
	m := newCircuitManagerWithSettings(circuitSettings{}, nopLogger{})

	for i := 0; i < 10; i++ {
		m.Record("m1", fmt.Errorf("500 internal"))
	}
	if !m.Allow("m1") {
		t.Fatal("disabled manager must always allow")
	}
	if m.AllUnavailable() {
		t.Fatal("disabled manager must never report all unavailable")
	}
}

// 成功会把窗口内失败计数重置——half-open 探测成功后连续失败需要重新累计。
func TestCircuitStateValues(t *testing.T) {
	m := newTestManager(t)
	const key = "m1"

	c := m.getOrCreate(key)
	if c.cb.State() != circuitbreaker.ClosedState {
		t.Fatalf("initial state = %v, want closed", c.cb.State())
	}

	m.Record(key, errors.New("403 quota exhausted"))
	if c.cb.State() != circuitbreaker.OpenState {
		t.Fatalf("state after hard error = %v, want open", c.cb.State())
	}
}
