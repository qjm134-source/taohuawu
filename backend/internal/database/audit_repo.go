package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// AuditRepository 审计日志仓储接口
type AuditRepository interface {
	Create(log *AuditLog) error
	GetByTenantID(tenantID string, startDate, endDate time.Time, page, pageSize int) ([]*AuditLog, int64, error)
	GetCountByTenantID(tenantID string, since time.Time) (int64, error)
}

type auditRepository struct {
	db *gorm.DB
}

// NewAuditRepository 创建审计日志仓储
func NewAuditRepository(db *gorm.DB) AuditRepository {
	return &auditRepository{db: db}
}

func (r *auditRepository) Create(log *AuditLog) error {
	if r.db == nil {
		return fmt.Errorf("database connection is nil")
	}

	// 检查日志数据
	if log.ID == "" {
		return fmt.Errorf("audit log ID is empty")
	}
	if log.TenantID == "" {
		return fmt.Errorf("audit log TenantID is empty")
	}
	if log.Action == "" {
		return fmt.Errorf("audit log Action is empty")
	}
	if log.Status == "" {
		return fmt.Errorf("audit log Status is empty")
	}

	result := r.db.Create(log)
	if result.Error != nil {
		return fmt.Errorf("failed to create audit log: %w", result.Error)
	}

	// 检查是否真的写入了
	if result.RowsAffected != 1 {
		return fmt.Errorf("expected 1 row affected, got %d", result.RowsAffected)
	}

	return nil
}

func (r *auditRepository) GetByTenantID(tenantID string, startDate, endDate time.Time, page, pageSize int) ([]*AuditLog, int64, error) {
	var logs []*AuditLog
	var total int64

	query := r.db.Model(&AuditLog{}).Where("tenant_id = ?", tenantID)

	if !startDate.IsZero() {
		query = query.Where("created_at >= ?", startDate)
	}
	if !endDate.IsZero() {
		query = query.Where("created_at <= ?", endDate)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	err := query.Order("created_at DESC").
		Limit(pageSize).
		Offset(offset).
		Find(&logs).Error

	return logs, total, err
}

func (r *auditRepository) GetCountByTenantID(tenantID string, since time.Time) (int64, error) {
	var count int64
	query := r.db.Model(&AuditLog{}).Where("tenant_id = ?", tenantID)
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since)
	}
	return count, query.Count(&count).Error
}
