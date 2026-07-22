package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/watertown/guide/internal/config"
	"github.com/watertown/guide/internal/database"
	"github.com/watertown/guide/internal/knowledge"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/internal/server"
	"github.com/watertown/guide/pkg/logging"
	"github.com/watertown/guide/pkg/utils"
)

func main() {
	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// 初始化日志
	var logger logging.Logger
	if cfg.Logging.File.Enabled {
		fileLogger, err := logging.NewFileLogger(logging.Config{
			Level:  cfg.Logging.Level,
			Format: cfg.Logging.Format,
		}, logging.FileLoggerConfig{
			Enabled:    cfg.Logging.File.Enabled,
			Path:       cfg.Logging.File.Path,
			MaxSize:    cfg.Logging.File.MaxSize,
			MaxBackups: cfg.Logging.File.MaxBackups,
			MaxAge:     cfg.Logging.File.MaxAge,
			Compress:   cfg.Logging.File.Compress,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to initialize file logger: %v\n", err)
			os.Exit(1)
		}
		defer fileLogger.Close()
		logger = fileLogger
	} else {
		logger = logging.New(logging.Config{
			Level:  cfg.Logging.Level,
			Format: cfg.Logging.Format,
		})
	}
	logger.Info("Starting Water Town Guide Server...")

	// 初始化数据库
	db, err := database.Init(cfg.Database)
	if err != nil {
		logger.Fatal("Failed to initialize database", "error", err)
	}

	// 加载知识库
	kb, err := knowledge.Load(cfg.Knowledge.Path)
	if err != nil {
		logger.Fatal("Failed to load knowledge base", "error", err)
	}
	logger.Info("Knowledge base loaded", "questions", len(kb.Categories))

	// 初始化 OpenTelemetry（包含 Langfuse OTLP 导出）
	fmt.Fprintf(os.Stderr, "[Langfuse] Config: Enabled=%v PublicKey=%q SecretKey=%q\n",
		cfg.Observability.Langfuse.Enabled,
		cfg.Observability.Langfuse.PublicKey,
		cfg.Observability.Langfuse.SecretKey)

	tp, err := observability.InitTracing(observability.ObservabilityConfig{
		Enabled:     cfg.Observability.Enabled,
		ServiceName: cfg.Observability.ServiceName,
		Endpoint:    cfg.Observability.Endpoint,
		SampleRate:  cfg.Observability.SampleRate,
		Exporter:    cfg.Observability.Exporter,
		Langfuse: observability.LangfuseConfig{
			Enabled:   cfg.Observability.Langfuse.Enabled,
			Host:      cfg.Observability.Langfuse.Host,
			PublicKey: cfg.Observability.Langfuse.PublicKey,
			SecretKey: cfg.Observability.Langfuse.SecretKey,
		},
	})
	if err != nil {
		logger.Warn("Failed to initialize tracing", "error", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tp.Shutdown(ctx)
		}()
	}

	// 初始化服务器
	srv, err := server.New(cfg, db, kb, logger)
	if err != nil {
		logger.Fatal("Failed to initialize server", "error", err)
	}

	// 启动服务器
	go func() {
		defer utils.RecoverWithCustomLogger("server", logger)
		if err := srv.Start(); err != nil {
			logger.Fatal("Server failed", "error", err)
		}
	}()

	logger.Info("Server started", "port", cfg.Server.Port)

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("Server shutdown error", "error", err)
	}

	logger.Info("Server stopped")
}
