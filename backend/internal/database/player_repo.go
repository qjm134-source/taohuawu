package database

import (
	"time"

	"gorm.io/gorm"
)

// PlayerRepository 玩家仓储接口
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

// NewPlayerRepository 创建玩家仓储
func NewPlayerRepository(db *gorm.DB) PlayerRepository {
	return &playerRepository{db: db}
}

func (r *playerRepository) Create(player *Player) error {
	return r.db.Create(player).Error
}

func (r *playerRepository) GetByID(id string) (*Player, error) {
	var player Player
	err := r.db.First(&player, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &player, nil
}

func (r *playerRepository) GetByDeviceID(deviceID, tenantID string) (*Player, error) {
	var player Player
	err := r.db.First(&player, "device_id = ? AND tenant_id = ?", deviceID, tenantID).Error
	if err != nil {
		return nil, err
	}
	return &player, nil
}

func (r *playerRepository) UpdateLastVisit(id string) error {
	return r.db.Model(&Player{}).Where("id = ?", id).Updates(map[string]interface{}{
		"last_visit_time": time.Now(),
	}).Error
}

func (r *playerRepository) IncrementDialogues(id string) error {
	return r.db.Model(&Player{}).Where("id = ?", id).UpdateColumn("total_dialogues", gorm.Expr("total_dialogues + 1")).Error
}
