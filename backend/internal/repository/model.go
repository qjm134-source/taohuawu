package repository

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// Player 玩家模型
type Player struct {
	ID             string    `gorm:"primaryKey;size:64"`
	TenantID       string    `gorm:"index;size:32;not null"`
	Nickname       string    `gorm:"size:64;not null"`
	DeviceID       string    `gorm:"index;size:128"`
	FirstVisitTime time.Time `gorm:"autoCreateTime"`
	LastVisitTime  time.Time `gorm:"autoCreateTime"`
	TotalDialogues int       `gorm:"default:0"`
	CreatedAt      time.Time `gorm:"autoCreateTime"`
	UpdatedAt      time.Time `gorm:"autoCreateTime"`
}

// Conversation 对话记录模型
type Conversation struct {
	ID          string `gorm:"primaryKey;size:64"`
	PlayerID    string `gorm:"index;size:64;not null"`
	TenantID    string `gorm:"index;size:32;not null"`
	SessionID   string `gorm:"index;size:64;not null"`
	UserMessage string `gorm:"type:text;not null"`
	AIMessage   string `gorm:"type:text;not null"`
	Emotion     string `gorm:"size:16"`
	ToolsUsed   JSON   `gorm:"type:json"`
	LLMModel    string `gorm:"size:32"`
	LLMTokens   int
	Cost        float64   `gorm:"type:decimal(10,6)"`
	CacheHit    bool      `gorm:"default:false"`
	CreatedAt   time.Time `gorm:"index;autoCreateTime"`
}

// AuditLog 审计日志模型
type AuditLog struct {
	ID              string `gorm:"primaryKey;size:64"`
	PlayerID        string `gorm:"size:64"`
	TenantID        string `gorm:"index;size:32;not null"`
	Action          string `gorm:"index;size:32;not null"`
	RequestPayload  JSON   `gorm:"type:json"`
	ResponsePayload JSON   `gorm:"type:json"`
	ErrorMessage    string `gorm:"type:text"`
	Status          string `gorm:"size:16;not null"` // success, error, timeout
	LatencyMs       int
	CreatedAt       time.Time `gorm:"index;autoCreateTime"`
}

// JSON 自定义类型用于存储 JSON 数据
type JSON struct {
	Data interface{}
}

// Scan 实现 sql.Scanner 接口
func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(bytes, &j.Data)
}

// Value 实现 driver.Valuer 接口
func (j JSON) Value() (driver.Value, error) {
	if j.Data == nil {
		return nil, nil
	}
	return json.Marshal(j.Data)
}
