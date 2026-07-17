package database

import (
	"time"

	"gorm.io/gorm"
)

// ConversationRepository 对话仓储接口
type ConversationRepository interface {
	Create(conv *Conversation) error
	GetByPlayerID(playerID string, limit, offset int) ([]*Conversation, error)
	GetBySessionID(sessionID string) ([]*Conversation, error)
	GetRecentHistory(playerID string, since time.Time) ([]*Conversation, error)
}

type conversationRepository struct {
	db *gorm.DB
}

// NewConversationRepository 创建对话仓储
func NewConversationRepository(db *gorm.DB) ConversationRepository {
	return &conversationRepository{db: db}
}

func (r *conversationRepository) Create(conv *Conversation) error {
	return r.db.Create(conv).Error
}

func (r *conversationRepository) GetByPlayerID(playerID string, limit, offset int) ([]*Conversation, error) {
	var conversations []*Conversation
	err := r.db.Where("player_id = ?", playerID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&conversations).Error
	return conversations, err
}

func (r *conversationRepository) GetBySessionID(sessionID string) ([]*Conversation, error) {
	var conversations []*Conversation
	err := r.db.Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Find(&conversations).Error
	return conversations, err
}

func (r *conversationRepository) GetRecentHistory(playerID string, since time.Time) ([]*Conversation, error) {
	var conversations []*Conversation
	err := r.db.Where("player_id = ? AND created_at > ?", playerID, since).
		Order("created_at ASC").
		Find(&conversations).Error
	return conversations, err
}
