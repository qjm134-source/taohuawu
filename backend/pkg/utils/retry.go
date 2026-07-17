package utils

import (
	"context"
	"time"
)

// RetryOption 重试选项
type RetryOption func(*retryConfig)

type retryConfig struct {
	maxRetries int
	delay      time.Duration
	multiplier float64
}

// WithMaxRetries 设置最大重试次数
func WithMaxRetries(n int) RetryOption {
	return func(c *retryConfig) {
		c.maxRetries = n
	}
}

// WithDelay 设置初始延迟
func WithDelay(d time.Duration) RetryOption {
	return func(c *retryConfig) {
		c.delay = d
	}
}

// WithMultiplier 设置延迟倍增因子
func WithMultiplier(m float64) RetryOption {
	return func(c *retryConfig) {
		c.multiplier = m
	}
}

// Retry 重试执行函数
func Retry(ctx context.Context, fn func() error, opts ...RetryOption) error {
	config := &retryConfig{
		maxRetries: 3,
		delay:      1 * time.Second,
		multiplier: 2.0,
	}

	for _, opt := range opts {
		opt(config)
	}

	var lastErr error
	for i := 0; i <= config.maxRetries; i++ {
		if i > 0 {
			select {
			case <-time.After(config.delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			config.delay = time.Duration(float64(config.delay) * config.multiplier)
		}

		if err := fn(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	return lastErr
}
