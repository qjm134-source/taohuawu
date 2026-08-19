package weather

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/watertown/guide/pkg/logging"
)

const (
	// defaultHTTPTimeout 是天气服务 HTTP 客户端默认超时。
	defaultHTTPTimeout = 30 * time.Second
	// defaultMaxIdleConns 是 HTTP 连接池最大空闲连接数。
	defaultMaxIdleConns = 10
	// defaultMaxIdleConnsPerHost 是每个 Host 最大空闲连接数。
	defaultMaxIdleConnsPerHost = 5
	// defaultIdleConnTimeout 是空闲连接超时时间。
	defaultIdleConnTimeout = 30 * time.Second
	// defaultRetryDelay 是重试间隔基数。
	defaultRetryDelay = 500 * time.Millisecond
)

var (
	ErrCityNotFound   = errors.New("city not found")
	ErrAPIKeyMissing  = errors.New("weather API key is missing")
	ErrProviderNotSet = errors.New("weather provider not configured")
)

type WeatherData struct {
	City         string  `json:"city"`
	Time         string  `json:"time"`
	TemperatureC float64 `json:"temperature_c"`
	Weather      string  `json:"weather"`
	WindSpeedKmh float64 `json:"wind_speed_kmh"`
}

type Service interface {
	GetWeather(ctx context.Context, city string) (*WeatherData, error)
}

type Config struct {
	Provider  string
	QWeather  QWeatherConfig
	OpenMeteo OpenMeteoConfig
}

type QWeatherConfig struct {
	APIKey     string
	BaseURL    string
	Timeout    time.Duration
	MaxRetries int
}

type OpenMeteoConfig struct {
	Timeout    time.Duration
	MaxRetries int
}

func NewService(cfg Config, logger logging.Logger) (Service, error) {
	switch cfg.Provider {
	case "qweather":
		if cfg.QWeather.APIKey == "" {
			return nil, ErrAPIKeyMissing
		}
		return NewQWeatherService(cfg.QWeather, logger), nil
	case "openmeteo":
		return NewOpenMeteoService(cfg.OpenMeteo, logger), nil
	default:
		return nil, ErrProviderNotSet
	}
}

// newWeatherHTTPClient 创建天气服务共用的 HTTP 客户端。
func newWeatherHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        defaultMaxIdleConns,
			MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
			IdleConnTimeout:     defaultIdleConnTimeout,
		},
	}
}

// retryDelay 计算第 attempt 次重试的等待时间（指数退避）。
func retryDelay(attempt int) time.Duration {
	return time.Duration(attempt) * defaultRetryDelay
}
