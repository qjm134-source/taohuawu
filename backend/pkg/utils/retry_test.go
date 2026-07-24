package utils

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	fn := func() error {
		calls++
		return nil
	}

	if err := Retry(context.Background(), fn); err != nil {
		t.Fatalf("Retry() = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestRetry_SuccessAfterFailures(t *testing.T) {
	calls := 0
	fn := func() error {
		calls++
		if calls < 3 {
			return errors.New("temporary error")
		}
		return nil
	}

	if err := Retry(context.Background(), fn, WithMaxRetries(5), WithDelay(10*time.Millisecond)); err != nil {
		t.Fatalf("Retry() = %v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRetry_MaxRetriesExceeded(t *testing.T) {
	wantErr := errors.New("persistent error")
	calls := 0
	fn := func() error {
		calls++
		return wantErr
	}

	err := Retry(context.Background(), fn, WithMaxRetries(2), WithDelay(10*time.Millisecond))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Retry() = %v, want %v", err, wantErr)
	}
	if calls != 3 { // 初始 1 次 + 2 次重试
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRetry_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fn := func() error {
		return errors.New("never succeeds")
	}

	err := Retry(ctx, fn, WithDelay(time.Hour))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry() = %v, want context.Canceled", err)
	}
}

func TestRetry_BackoffDelay(t *testing.T) {
	calls := 0
	fn := func() error {
		calls++
		if calls < 3 {
			return errors.New("temporary error")
		}
		return nil
	}

	start := time.Now()
	if err := Retry(context.Background(), fn, WithMaxRetries(5), WithDelay(10*time.Millisecond), WithMultiplier(2.0)); err != nil {
		t.Fatalf("Retry() = %v, want nil", err)
	}
	elapsed := time.Since(start)

	// 第一次失败后等待 10ms，第二次失败后等待 20ms
	minExpected := 25 * time.Millisecond
	if elapsed < minExpected {
		t.Errorf("elapsed = %v, want at least %v", elapsed, minExpected)
	}
}
