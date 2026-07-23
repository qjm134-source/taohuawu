package llm

import "context"

// contextKey 用于 context.WithValue 的私有类型，避免与第三方包的字符串 key 冲突。
type contextKey int

const (
	// sessionIDKey 存储 session_id 的 context key。
	sessionIDKey contextKey = iota
)

// ContextWithSessionID 将 sessionID 注入到 context 中。
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext 从 context 中提取 sessionID，若不存在或类型不匹配则返回 false。
func SessionIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(sessionIDKey).(string)
	return v, ok
}
