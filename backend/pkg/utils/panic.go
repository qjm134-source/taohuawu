package utils

import (
	"runtime"
)

// Logger 是能够输出 Errorf 的最小日志接口。
type Logger interface {
	Errorf(format string, args ...interface{})
}

// RecoverWithCustomLogger 使用传入的 logger 捕获并记录 panic 堆栈。
func RecoverWithCustomLogger(component string, logger Logger) {
	if r := recover(); r != nil {
		buf := make([]byte, 4096)
		n := runtime.Stack(buf, false)
		logger.Errorf("[PanicRecovery] component=%s panic=%v\n%s", component, r, buf[:n])
	}
}

// RecoverWithCustomLoggerFunc 返回一个 defer 可用的 panic 恢复函数。
func RecoverWithCustomLoggerFunc(component string, logger Logger) func() {
	return func() {
		RecoverWithCustomLogger(component, logger)
	}
}

// SafeGoWithLogger 在带有 logger 的 panic 恢复的 goroutine 中执行 fn。
func SafeGoWithLogger(component string, logger Logger, fn func()) {
	go func() {
		defer RecoverWithCustomLogger(component, logger)
		fn()
	}()
}
