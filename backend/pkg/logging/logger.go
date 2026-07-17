package logging

import (
	"io"
	"os"

	"github.com/sirupsen/logrus"
)

// Logger 日志接口
type Logger interface {
	Debug(args ...interface{})
	Debugf(format string, args ...interface{})
	Info(args ...interface{})
	Infof(format string, args ...interface{})
	Warn(args ...interface{})
	Warnf(format string, args ...interface{})
	Error(args ...interface{})
	Errorf(format string, args ...interface{})
	Fatal(args ...interface{})
	Fatalf(format string, args ...interface{})
	WithFields(fields logrus.Fields) *logrus.Entry
}

type logger struct {
	*logrus.Logger
}

// 确保 logger 实现了 FileLogger 接口的一部分
var _ Logger = (*logger)(nil)

// Config 日志配置
type Config struct {
	Level  string
	Format string // json or text
}

// FileLoggerConfig 文件日志配置
type FileLoggerConfig struct {
	Enabled    bool
	Path       string
	MaxSize    int // MB
	MaxBackups int
	MaxAge     int // days
	Compress   bool
}

// New 创建日志实例
func New(cfg Config) Logger {
	log := logrus.New()

	// 设置日志级别
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	log.SetLevel(level)

	// 设置输出格式
	log.SetFormatter(&CustomFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
	})

	// 设置输出
	log.SetOutput(io.MultiWriter(os.Stdout))

	return &logger{Logger: log}
}
