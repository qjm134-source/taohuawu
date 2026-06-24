package database

import (
	"fmt"
	"os"
	"strings"

	"github.com/watertown/guide/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Init 初始化数据库连接
func Init(cfg config.DatabaseConfig) (*gorm.DB, error) {
	// 优先使用 DATABASE_URL 环境变量（Render 等云平台提供）
	databaseURL := os.Getenv("DATABASE_URL")

	var db *gorm.DB
	var err error

	if databaseURL != "" {
		// 使用 DATABASE_URL 连接（支持 PostgreSQL）
		db, err = connectWithDatabaseURL(databaseURL)
	} else {
		// 使用传统配置连接（支持 MySQL）
		dsn := cfg.GetDSN()
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Error),
		})
	}

	if err != nil {
		return nil, err
	}

	// 自动迁移表
	if err := db.AutoMigrate(&Player{}, &Conversation{}, &AuditLog{}); err != nil {
		return nil, err
	}

	return db, nil
}

// connectWithDatabaseURL 使用 DATABASE_URL 连接数据库
func connectWithDatabaseURL(databaseURL string) (*gorm.DB, error) {
	// 判断数据库类型
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		// PostgreSQL 连接
		return gorm.Open(postgres.Open(databaseURL), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Error),
		})
	} else if strings.Contains(databaseURL, "mysql") {
		// MySQL 连接
		return gorm.Open(mysql.Open(databaseURL), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Error),
		})
	}

	return nil, fmt.Errorf("unsupported database URL format: %s", databaseURL)
}
