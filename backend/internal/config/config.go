package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Database      DatabaseConfig      `yaml:"database"`
	WebSocket     WebSocketConfig     `yaml:"websocket"`
	LLM           LLMConfig           `yaml:"llm"`
	Circuit       CircuitConfig       `yaml:"circuit"`
	Cost          CostConfig          `yaml:"cost"`
	Knowledge     KnowledgeConfig     `yaml:"knowledge"`
	Logging       LoggingConfig       `yaml:"logging"`
	Observability ObservabilityConfig `yaml:"observability"`
}

type ServerConfig struct {
	Port         int          `yaml:"port"`
	ReadTimeout  timeDuration `yaml:"read_timeout"`
	WriteTimeout timeDuration `yaml:"write_timeout"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	SSLMode  string `yaml:"ssl_mode"`
}

type WebSocketConfig struct {
	Path             string       `yaml:"path"`
	PingInterval     timeDuration `yaml:"ping_interval"`
	PongWait         timeDuration `yaml:"pong_wait"`
	WriteWait        timeDuration `yaml:"write_wait"`
	MessageSizeLimit int64        `yaml:"message_size_limit"`
}

// ModelConfig 单个模型配置
type ModelConfig struct {
	Name        string  `yaml:"name"`
	BaseURL     string  `yaml:"base_url"`
	APIKey      string  `yaml:"api_key"`
	Enabled     bool    `yaml:"enabled"`
	MaxTokens   int     `yaml:"max_tokens"`
	Temperature float64 `yaml:"temperature"`
}

type LLMConfig struct {
	Models     []ModelConfig `yaml:"models"`
	Timeout    timeDuration  `yaml:"timeout"`
	MaxRetries int           `yaml:"max_retries"`
	RetryDelay timeDuration  `yaml:"retry_delay"`
	AutoSwitch bool          `yaml:"auto_switch"`
}

type CircuitConfig struct {
	MaxFailures   int          `yaml:"max_failures"`
	FailureWindow timeDuration `yaml:"failure_window"`
	RecoveryTime  timeDuration `yaml:"recovery_time"`
	HalfOpenLimit int          `yaml:"half_open_limit"`
}

type CostConfig struct {
	MaxHistoryMessages  int          `yaml:"max_history_messages"`
	SummaryThreshold    int          `yaml:"summary_threshold"`
	SimilarityThreshold float64      `yaml:"similarity_threshold"`
	CacheTTL            timeDuration `yaml:"cache_ttl"`
}

type KnowledgeConfig struct {
	Path string `yaml:"path"`
}

type LoggingConfig struct {
	Level  string           `yaml:"level"`
	Format string           `yaml:"format"`
	File   FileLoggerConfig `yaml:"file"`
}

type FileLoggerConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Path       string `yaml:"path"`
	MaxSize    int    `yaml:"max_size"` // MB
	MaxBackups int    `yaml:"max_backups"`
	MaxAge     int    `yaml:"max_age"` // days
	Compress   bool   `yaml:"compress"`
}

type ObservabilityConfig struct {
	Enabled     bool    `yaml:"enabled"`
	ServiceName string  `yaml:"service_name"`
	Endpoint    string  `yaml:"endpoint"`
	SampleRate  float64 `yaml:"sample_rate"`
}

// timeDuration 包装 time.Duration 以支持 YAML 解析
type timeDuration struct {
	time.Duration
}

func (d *timeDuration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	dur, err := parseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

func parseDuration(s string) (time.Duration, error) {
	// 简单解析，支持 30s, 500ms, 1m, 1h 等格式
	switch {
	case len(s) == 0:
		return 0, fmt.Errorf("empty duration")
	case s[len(s)-1] == 's':
		sec, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(sec * float64(time.Second)), nil
	case s[len(s)-1] == 'm':
		min, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(min * float64(time.Minute)), nil
	case s[len(s)-1] == 'h':
		hour, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(hour * float64(time.Hour)), nil
	case len(s) > 2 && s[len(s)-2:] == "ms":
		ms, err := strconv.ParseFloat(s[:len(s)-2], 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(ms * float64(time.Millisecond)), nil
	default:
		return 0, fmt.Errorf("invalid duration format: %s", s)
	}
}

func (d timeDuration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

// Load 加载配置
func Load() (*Config, error) {
	// 加载 .env 文件（如果存在），Render 等环境通过环境变量直接注入
	_ = godotenv.Load()

	cfg := &Config{}



	// 从 YAML 文件加载基础配置
	configFile := os.Getenv("CONFIG_FILE")
	if configFile == "" {
		configFile = "configs/config.yaml"
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// 从环境变量覆盖敏感配置
	// 处理各个模型的 API Key
	for i := range cfg.LLM.Models {
		if strings.HasPrefix(cfg.LLM.Models[i].APIKey, "${") && strings.HasSuffix(cfg.LLM.Models[i].APIKey, "}") {
			// 提取环境变量名称，例如 ${MIMO_API_KEY} -> MIMO_API_KEY
			envName := cfg.LLM.Models[i].APIKey[2 : len(cfg.LLM.Models[i].APIKey)-1]
			if envValue := os.Getenv(envName); envValue != "" {
				cfg.LLM.Models[i].APIKey = envValue
			} else {
				// 环境变量未设置，记录警告
				fmt.Printf("Warning: environment variable %s is not set for model %s\n", envName, cfg.LLM.Models[i].Name)
			}
		}
	}

	if dbPass := os.Getenv("DB_PASSWORD"); dbPass != "" {
		cfg.Database.Password = dbPass
	}

	if dbHost := os.Getenv("DB_HOST"); dbHost != "" {
		cfg.Database.Host = dbHost
	}

	if dbPort := os.Getenv("DB_PORT"); dbPort != "" {
		if port, err := strconv.Atoi(dbPort); err == nil {
			cfg.Database.Port = port
		}
	}

	return cfg, nil
}

// GetDSN 获取数据库连接字符串
func (c *DatabaseConfig) GetDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		c.User, c.Password, c.Host, c.Port, c.Name)
}
