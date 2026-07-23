package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PlayerRepository 玩家仓储接口。
type PlayerRepository interface {
	Create(player *Player) error
	GetByID(id string) (*Player, error)
	GetByDeviceID(deviceID, tenantID string) (*Player, error)
	UpdateLastVisit(id string) error
	IncrementDialogues(id string) error
}

type playerRepository struct {
	db *gorm.DB
}

// NewPlayerRepository 创建玩家仓储。
func NewPlayerRepository(db *gorm.DB) PlayerRepository {
	return &playerRepository{db: db}
}

// Create 创建玩家记录。
func (r *playerRepository) Create(player *Player) error {
	if err := r.db.Create(player).Error; err != nil {
		return fmt.Errorf("create player: %w", err)
	}
	return nil
}

// GetByID 根据 ID 获取玩家。
func (r *playerRepository) GetByID(id string) (*Player, error) {
	var player Player
	if err := r.db.First(&player, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("get player by id %s: %w", id, err)
	}
	return &player, nil
}

// GetByDeviceID 根据设备 ID 和租户 ID 获取玩家。
func (r *playerRepository) GetByDeviceID(deviceID, tenantID string) (*Player, error) {
	var player Player
	if err := r.db.First(&player, "device_id = ? AND tenant_id = ?", deviceID, tenantID).Error; err != nil {
		return nil, fmt.Errorf("get player by device id %s: %w", deviceID, err)
	}
	return &player, nil
}

// UpdateLastVisit 更新玩家最近访问时间。
func (r *playerRepository) UpdateLastVisit(id string) error {
	if err := r.db.Model(&Player{}).Where("id = ?", id).Updates(map[string]interface{}{
		"last_visit_time": time.Now(),
	}).Error; err != nil {
		return fmt.Errorf("update player %s last visit: %w", id, err)
	}
	return nil
}

// IncrementDialogues 增加玩家对话计数。
func (r *playerRepository) IncrementDialogues(id string) error {
	if err := r.db.Model(&Player{}).Where("id = ?", id).UpdateColumn("total_dialogues", gorm.Expr("total_dialogues + 1")).Error; err != nil {
		return fmt.Errorf("increment player %s dialogues: %w", id, err)
	}
	return nil
}
