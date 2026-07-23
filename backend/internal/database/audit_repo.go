package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// AuditRepository 审计日志仓储接口。
type AuditRepository interface {
	Create(log *AuditLog) error
	GetByTenantID(tenantID string, startDate, endDate time.Time, page, pageSize int) ([]*AuditLog, int64, error)
	GetCountByTenantID(tenantID string, since time.Time) (int64, error)
}

type auditRepository struct {
	db *gorm.DB
}

// NewAuditRepository 创建审计日志仓储。
func NewAuditRepository(db *gorm.DB) AuditRepository {
	return &auditRepository{db: db}
}

// Create 写入一条审计日志。
func (r *auditRepository) Create(log *AuditLog) error {
	if r.db == nil {
		return fmt.Errorf("database connection is nil")
	}

	if log.ID == "" {
		return fmt.Errorf("audit log id is empty")
	}
	if log.TenantID == "" {
		return fmt.Errorf("audit log tenant id is empty")
	}
	if log.Action == "" {
		return fmt.Errorf("audit log action is empty")
	}
	if log.Status == "" {
		return fmt.Errorf("audit log status is empty")
	}

	result := r.db.Create(log)
	if result.Error != nil {
		return fmt.Errorf("create audit log: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("expected 1 row affected, got %d", result.RowsAffected)
	}

	return nil
}

// GetByTenantID 分页查询租户审计日志。
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
		return nil, 0, fmt.Errorf("count audit logs for tenant %s: %w", tenantID, err)
	}

	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").
		Limit(pageSize).
		Offset(offset).
		Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("get audit logs for tenant %s: %w", tenantID, err)
	}

	return logs, total, nil
}

// GetCountByTenantID 查询租户指定时间以来的审计日志数量。
func (r *auditRepository) GetCountByTenantID(tenantID string, since time.Time) (int64, error) {
	var count int64
	query := r.db.Model(&AuditLog{}).Where("tenant_id = ?", tenantID)
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count audit logs for tenant %s: %w", tenantID, err)
	}
	return count, nil
}
