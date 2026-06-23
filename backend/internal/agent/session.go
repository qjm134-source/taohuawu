package agent

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Session 会话
type Session struct {
	ID           string
	PlayerID     string
	TenantID     string
	Nickname     string
	IsFirstVisit bool
	CreatedAt    time.Time
	LastActive   time.Time
	Messages     []Message
	Context      map[string]interface{}
	mu           sync.RWMutex
}

// Message 消息
type Message struct {
	Role      string // user | assistant | system
	Content   string
	Timestamp time.Time
	Emotion   string
	Tools     []ToolCall
}

// ToolCall 工具调用
type ToolCall struct {
	Name   string
	Params map[string]interface{}
	Result interface{}
}

// SessionManager 会话管理器
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// NewSessionManager 创建会话管理器
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// GetOrCreate 获取或创建会话
func (sm *SessionManager) GetOrCreate(playerID, tenantID string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, session := range sm.sessions {
		if session.PlayerID == playerID && session.TenantID == tenantID {
			session.LastActive = time.Now()
			return session
		}
	}

	session := &Session{
		ID:           uuid.New().String(),
		PlayerID:     playerID,
		TenantID:     tenantID,
		Nickname:     "玩家",
		IsFirstVisit: true,
		CreatedAt:    time.Now(),
		LastActive:   time.Now(),
		Messages:     make([]Message, 0),
		Context:      make(map[string]interface{}),
	}

	sm.sessions[session.ID] = session
	return session
}

// Get 获取会话
func (sm *SessionManager) Get(sessionID string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[sessionID]
	return session, ok
}

// Remove 移除会话
func (sm *SessionManager) Remove(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.sessions, sessionID)
}

// AddMessage 添加消息
func (s *Session) AddMessage(role, content string, emotion string, tools []ToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Messages = append(s.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
		Emotion:   emotion,
		Tools:     tools,
	})

	s.LastActive = time.Now()
}

// GetMessages 获取消息
func (s *Session) GetMessages(limit int) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.Messages) {
		limit = len(s.Messages)
	}

	start := len(s.Messages) - limit
	return s.Messages[start:]
}

// MarkVisited 标记已访问
func (s *Session) MarkVisited() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.IsFirstVisit = false
}

// UpdateNickname 更新昵称
func (s *Session) UpdateNickname(nickname string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Nickname = nickname
}
