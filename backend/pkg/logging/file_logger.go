package logging

import (
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

type FileLogger struct {
	*logrus.Logger
	fileHook *lumberjack.Logger
}

// NewFileLogger 创建支持文件输出的日志器（只输出到文件，不输出到控制台）
func NewFileLogger(cfg Config, fileCfg FileLoggerConfig) (*FileLogger, error) {
	logger := logrus.New()

	// 设置日志格式
	if cfg.Format == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
		})
	} else {
		logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02T15:04:05",
		})
	}

	// 设置日志级别
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)

	// 添加文件输出
	if fileCfg.Enabled && fileCfg.Path != "" {
		// 确保日志目录存在
		if err := os.MkdirAll(filepath.Dir(fileCfg.Path), 0755); err != nil {
			return nil, err
		}

		// 创建日志文件轮转器
		fileHook := &lumberjack.Logger{
			Filename:   fileCfg.Path,
			MaxSize:    fileCfg.MaxSize,
			MaxBackups: fileCfg.MaxBackups,
			MaxAge:     fileCfg.MaxAge,
			Compress:   fileCfg.Compress,
		}

		// 设置输出到文件，禁用控制台输出
		logger.SetOutput(fileHook)

		return &FileLogger{
			Logger:   logger,
			fileHook: fileHook,
		}, nil
	}

	// 如果未启用文件日志，输出到空设备（不打印到控制台）
	logger.SetOutput(io.Discard)

	return &FileLogger{
		Logger: logger,
	}, nil
}

// Close 关闭文件hook
func (fl *FileLogger) Close() error {
	if fl.fileHook != nil {
		return fl.fileHook.Close()
	}
	return nil
}
