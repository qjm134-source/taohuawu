package weather

import (
	"context"
	"errors"
	"time"

	"github.com/watertown/guide/pkg/logging"
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
