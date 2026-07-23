package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ConversationRepository 对话仓储接口。
type ConversationRepository interface {
	Create(conv *Conversation) error
	GetByPlayerID(playerID string, limit, offset int) ([]*Conversation, error)
	GetBySessionID(sessionID string) ([]*Conversation, error)
	GetRecentHistory(playerID string, since time.Time) ([]*Conversation, error)
}

type conversationRepository struct {
	db *gorm.DB
}

// NewConversationRepository 创建对话仓储。
func NewConversationRepository(db *gorm.DB) ConversationRepository {
	return &conversationRepository{db: db}
}

// Create 创建对话记录。
func (r *conversationRepository) Create(conv *Conversation) error {
	if err := r.db.Create(conv).Error; err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}
	return nil
}

// GetByPlayerID 根据玩家 ID 分页获取对话记录。
func (r *conversationRepository) GetByPlayerID(playerID string, limit, offset int) ([]*Conversation, error) {
	var conversations []*Conversation
	if err := r.db.Where("player_id = ?", playerID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&conversations).Error; err != nil {
		return nil, fmt.Errorf("get conversations by player %s: %w", playerID, err)
	}
	return conversations, nil
}

// GetBySessionID 根据会话 ID 获取对话记录。
func (r *conversationRepository) GetBySessionID(sessionID string) ([]*Conversation, error) {
	var conversations []*Conversation
	if err := r.db.Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Find(&conversations).Error; err != nil {
		return nil, fmt.Errorf("get conversations by session %s: %w", sessionID, err)
	}
	return conversations, nil
}

// GetRecentHistory 获取玩家指定时间以来的对话历史。
func (r *conversationRepository) GetRecentHistory(playerID string, since time.Time) ([]*Conversation, error) {
	var conversations []*Conversation
	if err := r.db.Where("player_id = ? AND created_at > ?", playerID, since).
		Order("created_at ASC").
		Find(&conversations).Error; err != nil {
		return nil, fmt.Errorf("get recent history for player %s: %w", playerID, err)
	}
	return conversations, nil
}
