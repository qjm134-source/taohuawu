package database

import (
	"fmt"
	"os"
	"strings"

	"github.com/watertown/guide/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
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
		// 使用 DATABASE_URL 连接
		db, err = connectWithDatabaseURL(databaseURL)
	} else {
		// 根据配置的数据库类型连接
		switch strings.ToLower(cfg.Type) {
		case "sqlite":
			db, err = connectSQLite(cfg)
		case "postgres":
			db, err = connectPostgres(cfg)
		case "mysql":
			fallthrough
		default:
			db, err = connectMySQL(cfg)
		}
	}

	if err != nil {
		// MySQL 连接失败时，自动 fallback 到 SQLite
		if strings.ToLower(cfg.Type) == "mysql" || cfg.Type == "" {
			return connectSQLiteWithFallback(cfg)
		}
		return nil, err
	}

	// 自动迁移表
	if err := db.AutoMigrate(&Player{}, &Conversation{}, &AuditLog{}); err != nil {
		return nil, err
	}

	return db, nil
}

// connectMySQL 连接 MySQL 数据库
func connectMySQL(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dsn := cfg.GetDSN()
	return gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Error),
	})
}

// connectPostgres 连接 PostgreSQL 数据库
func connectPostgres(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name, cfg.SSLMode)
	return gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Error),
	})
}

// connectSQLite 连接 SQLite 数据库
func connectSQLite(cfg config.DatabaseConfig) (*gorm.DB, error) {
	path := cfg.Path
	if path == "" {
		path = "data/database/water_town.db"
	}

	// 确保目录存在
	if err := ensureDir(path); err != nil {
		return nil, err
	}

	return gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Error),
	})
}

// connectSQLiteWithFallback MySQL 连接失败时 fallback 到 SQLite
func connectSQLiteWithFallback(cfg config.DatabaseConfig) (*gorm.DB, error) {
	fmt.Println("[WARN] MySQL connection failed, falling back to SQLite")

	path := cfg.Path
	if path == "" {
		path = "data/database/water_town.db"
	}

	// 确保目录存在
	if err := ensureDir(path); err != nil {
		return nil, err
	}

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Error),
	})
	if err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(&Player{}, &Conversation{}, &AuditLog{}); err != nil {
		return nil, err
	}

	return db, nil
}

// ensureDir 确保目录存在
func ensureDir(path string) error {
	idx := strings.LastIndex(path, "/")
	if idx == -1 {
		return nil
	}
	dir := path[:idx]
	return os.MkdirAll(dir, 0755)
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
