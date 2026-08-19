package contextutil

import (
	"context"
	"testing"
)

func TestWithSessionID(t *testing.T) {
	ctx := context.Background()
	ctx = WithSessionID(ctx, "session-123")
	got, ok := SessionIDFromContext(ctx)
	if !ok {
		t.Fatal("SessionIDFromContext() ok = false, want true")
	}
	if got != "session-123" {
		t.Errorf("SessionIDFromContext() = %q, want %q", got, "session-123")
	}
}

func TestSessionIDFromContext_Empty(t *testing.T) {
	ctx := context.Background()
	got, ok := SessionIDFromContext(ctx)
	if ok {
		t.Errorf("SessionIDFromContext() ok = true, want false")
	}
	if got != "" {
		t.Errorf("SessionIDFromContext() = %q, want empty", got)
	}
}

func TestSessionIDFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), sessionIDKey, 123)
	got, ok := SessionIDFromContext(ctx)
	if ok {
		t.Errorf("SessionIDFromContext() ok = true, want false for wrong type")
	}
	if got != "" {
		t.Errorf("SessionIDFromContext() = %q, want empty", got)
	}
}

func TestSessionIDFromContext_Chain(t *testing.T) {
	ctx := context.Background()
	ctx = WithSessionID(ctx, "first")
	ctx = WithSessionID(ctx, "second")
	got, ok := SessionIDFromContext(ctx)
	if !ok {
		t.Fatal("SessionIDFromContext() ok = false, want true")
	}
	if got != "second" {
		t.Errorf("SessionIDFromContext() = %q, want %q (last value)", got, "second")
	}
}
