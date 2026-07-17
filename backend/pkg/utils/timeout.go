package utils

import (
	"context"
	"time"
)

// WithTimeout 创建带超时的上下文
func WithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// WithTimeoutFrom 从现有上下文创建带超时的上下文
func WithTimeoutFrom(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}
