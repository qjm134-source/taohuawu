package utils

import (
	"runtime"

	"github.com/sirupsen/logrus"
)

func RecoverWithLog(component string) {
	if r := recover(); r != nil {
		buf := make([]byte, 4096)
		n := runtime.Stack(buf, false)
		logrus.Errorf("[PanicRecovery] component=%s panic=%v\n%s", component, r, buf[:n])
	}
}

func RecoverWithLogFunc(component string) func() {
	return func() {
		RecoverWithLog(component)
	}
}

func RecoverWithCustomLogger(component string, logger interface {
	Errorf(format string, args ...interface{})
}) {
	if r := recover(); r != nil {
		buf := make([]byte, 4096)
		n := runtime.Stack(buf, false)
		logger.Errorf("[PanicRecovery] component=%s panic=%v\n%s", component, r, buf[:n])
	}
}

func RecoverWithCustomLoggerFunc(component string, logger interface {
	Errorf(format string, args ...interface{})
}) func() {
	return func() {
		RecoverWithCustomLogger(component, logger)
	}
}

func SafeGo(component string, fn func()) {
	go func() {
		defer RecoverWithLog(component)
		fn()
	}()
}

func SafeGoWithLogger(component string, logger interface {
	Errorf(format string, args ...interface{})
}, fn func()) {
	go func() {
		defer RecoverWithCustomLogger(component, logger)
		fn()
	}()
}
