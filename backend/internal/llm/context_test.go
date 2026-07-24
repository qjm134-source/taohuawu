package llm

import (
	"context"
	"testing"
)

func TestContextWithSessionID(t *testing.T) {
	ctx := context.Background()
	sessionID := "test-session-123"

	ctx = ContextWithSessionID(ctx, sessionID)
	got, ok := SessionIDFromContext(ctx)
	if !ok {
		t.Fatal("SessionIDFromContext() returned false, want true")
	}
	if got != sessionID {
		t.Errorf("SessionIDFromContext() = %q, want %q", got, sessionID)
	}
}

func TestSessionIDFromContext_EmptyContext(t *testing.T) {
	ctx := context.Background()
	got, ok := SessionIDFromContext(ctx)
	if ok {
		t.Errorf("SessionIDFromContext() = %q, ok = true, want ok = false", got)
	}
	if got != "" {
		t.Errorf("SessionIDFromContext() = %q, want empty string", got)
	}
}

func TestSessionIDFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), sessionIDKey, 123)
	got, ok := SessionIDFromContext(ctx)
	if ok {
		t.Errorf("SessionIDFromContext() = %q, ok = true, want ok = false", got)
	}
	if got != "" {
		t.Errorf("SessionIDFromContext() = %q, want empty string", got)
	}
}

func TestSessionIDFromContext_Chain(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, "other_key", "other_value")
	ctx = ContextWithSessionID(ctx, "test-session")

	got, ok := SessionIDFromContext(ctx)
	if !ok {
		t.Fatal("SessionIDFromContext() returned false, want true")
	}
	if got != "test-session" {
		t.Errorf("SessionIDFromContext() = %q, want %q", got, "test-session")
	}
}