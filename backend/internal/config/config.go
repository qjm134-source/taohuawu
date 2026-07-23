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
	Weather       WeatherConfig       `yaml:"weather"`
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
	Models           []ModelConfig    `yaml:"models"`
	Timeout          timeDuration     `yaml:"timeout"`
	MaxRetries       int              `yaml:"max_retries"`
	RetryDelay       timeDuration     `yaml:"retry_delay"`
	AutoSwitch       bool             `yaml:"auto_switch"`
	Strategy         string           `yaml:"strategy"` // 路由策略: fixed/cost/latency/capability/fallback/weighted
	FallbackResponse FallbackResponse `yaml:"fallback_response"`
}

type FallbackResponse struct {
	Enabled        bool   `yaml:"enabled"`
	WelcomeMessage string `yaml:"welcome_message"`
}

type CircuitConfig struct {
	MaxFailures   int          `yaml:"max_failures"`
	FailureWindow timeDuration `yaml:"failure_window"`
	RecoveryTime  timeDuration `yaml:"recovery_time"`
	HalfOpenLimit int          `yaml:"half_open_limit"`
}

type CostConfig struct {
	MaxHistoryMessages  int             `yaml:"max_history_messages"`
	MaxHistoryTokens    int             `yaml:"max_history_tokens"`
	SummaryThreshold    int             `yaml:"summary_threshold"`
	SimilarityThreshold float64         `yaml:"similarity_threshold"`
	CacheTTL            timeDuration    `yaml:"cache_ttl"`
	SummaryModel        string          `yaml:"summary_model"`   // 指定用于摘要的模型，留空则使用第一个启用的模型
	SummaryTimeout      timeDuration    `yaml:"summary_timeout"` // 摘要请求超时时间
	Embedding           EmbeddingConfig `yaml:"embedding"`       // Embedding API 配置
}

type EmbeddingConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Type       string `yaml:"type"` // local 或 remote
	APIKey     string `yaml:"api_key"`
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`
	ServerType string `yaml:"server_type"` // local 后端类型: "ollama" / "tei" / "openai-compat"
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
	Enabled     bool           `yaml:"enabled"`
	ServiceName string         `yaml:"service_name"`
	Endpoint    string         `yaml:"endpoint"`
	SampleRate  float64        `yaml:"sample_rate"`
	Exporter    string         `yaml:"trace_exporter"` // "otlp"（默认）或 "stdout"
	Langfuse    LangfuseConfig `yaml:"langfuse"`
	Prometheus  bool           `yaml:"prometheus"` // 是否启用 Prometheus 指标
}

// LangfuseConfig Langfuse LLM 可观测配置
type LangfuseConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Host      string `yaml:"host"`
	PublicKey string `yaml:"public_key"`
	SecretKey string `yaml:"secret_key"`
}

type WeatherConfig struct {
	Provider  string          `yaml:"provider"`
	QWeather  QWeatherConfig  `yaml:"qweather"`
	OpenMeteo OpenMeteoConfig `yaml:"openmeteo"`
}

type QWeatherConfig struct {
	APIKey     string       `yaml:"api_key"`
	BaseURL    string       `yaml:"base_url"`
	Timeout    timeDuration `yaml:"timeout"`
	MaxRetries int          `yaml:"max_retries"`
}

type OpenMeteoConfig struct {
	Timeout    timeDuration `yaml:"timeout"`
	MaxRetries int          `yaml:"max_retries"`
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

// Load 加载配置。
// 依次从 YAML 文件读取基础配置，再通过环境变量覆盖敏感字段。
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg, err := loadFromFile(configFilePath())
	if err != nil {
		return nil, err
	}

	cfg.resolveModelAPIKeys()
	cfg.resolveLangfuseKeys()
	cfg.resolveDatabase()
	cfg.resolveObservability()
	cfg.resolveWeatherAndEmbedding()

	return cfg, nil
}

func configFilePath() string {
	if path := os.Getenv("CONFIG_FILE"); path != "" {
		return path
	}
	return "configs/config.yaml"
}

func loadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return cfg, nil
}

// resolveEnvVar 解析形如 ${ENV_NAME} 的环境变量占位符。
// 若占位符不包含默认值且环境变量未设置，则返回原始值。
func resolveEnvVar(value string) string {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return value
	}

	envName := value[2 : len(value)-1]
	if idx := strings.Index(envName, ":"); idx != -1 {
		envName = envName[:idx]
	}

	if envValue := os.Getenv(envName); envValue != "" {
		return envValue
	}
	return value
}

func (cfg *Config) resolveModelAPIKeys() {
	for i := range cfg.LLM.Models {
		cfg.LLM.Models[i].APIKey = resolveEnvVar(cfg.LLM.Models[i].APIKey)
	}
}

func (cfg *Config) resolveLangfuseKeys() {
	cfg.Observability.Langfuse.PublicKey = resolveEnvVar(cfg.Observability.Langfuse.PublicKey)
	cfg.Observability.Langfuse.SecretKey = resolveEnvVar(cfg.Observability.Langfuse.SecretKey)
}

func (cfg *Config) resolveDatabase() {
	if dbPass := os.Getenv("DB_PASSWORD"); dbPass != "" {
		cfg.Database.Password = dbPass
	}
	if dbUser := os.Getenv("DB_USER"); dbUser != "" {
		cfg.Database.User = dbUser
	}
	if dbHost := os.Getenv("DB_HOST"); dbHost != "" {
		cfg.Database.Host = dbHost
	}
	if dbPort := os.Getenv("DB_PORT"); dbPort != "" {
		if port, err := strconv.Atoi(dbPort); err == nil {
			cfg.Database.Port = port
		}
	}
	if dbName := os.Getenv("DB_NAME"); dbName != "" {
		cfg.Database.Name = dbName
	}
}

func (cfg *Config) resolveObservability() {
	if obsEnabled := os.Getenv("OBSERVABILITY_ENABLED"); obsEnabled != "" {
		if enabled, err := strconv.ParseBool(obsEnabled); err == nil {
			cfg.Observability.Enabled = enabled
		}
	}
	if obsExporter := os.Getenv("OBSERVABILITY_TRACER_EXPORTER"); obsExporter != "" {
		cfg.Observability.Exporter = obsExporter
	}
	if obsEndpoint := os.Getenv("OBSERVABILITY_ENDPOINT"); obsEndpoint != "" {
		cfg.Observability.Endpoint = obsEndpoint
	}
	if obsPrometheus := os.Getenv("OBSERVABILITY_PROMETHEUS"); obsPrometheus != "" {
		if enabled, err := strconv.ParseBool(obsPrometheus); err == nil {
			cfg.Observability.Prometheus = enabled
		}
	}
}

func (cfg *Config) resolveWeatherAndEmbedding() {
	cfg.Weather.QWeather.APIKey = resolveEnvVar(cfg.Weather.QWeather.APIKey)
	cfg.Cost.Embedding.APIKey = resolveEnvVar(cfg.Cost.Embedding.APIKey)
}

// GetDSN 获取数据库连接字符串
func (c *DatabaseConfig) GetDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		c.User, c.Password, c.Host, c.Port, c.Name)
}
