package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/watertown/guide/pkg/logging"
)

type openMeteoService struct {
	client     *http.Client
	maxRetries int
	logger     logging.Logger
}

func NewOpenMeteoService(cfg OpenMeteoConfig, logger logging.Logger) Service {
	return &openMeteoService{
		maxRetries: cfg.MaxRetries,
		logger:     logger,
		client: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

func (s *openMeteoService) GetWeather(ctx context.Context, city string) (*WeatherData, error) {
	startTime := time.Now()
	s.logger.Infof("[Weather] [OpenMeteo] Start: city=%s", city)

	lat, lon, err := s.geocode(ctx, city)
	if err != nil {
		s.logger.Errorf("[Weather] [OpenMeteo] Failed: city=%s, latency=%dms, error=%v", city, time.Since(startTime).Milliseconds(), err)
		return nil, fmt.Errorf("failed to geocode city %s: %w", city, err)
	}

	weather, err := s.queryWeather(ctx, lat, lon)
	if err != nil {
		s.logger.Errorf("[Weather] [OpenMeteo] Failed: city=%s, latency=%dms, error=%v", city, time.Since(startTime).Milliseconds(), err)
		return nil, fmt.Errorf("failed to query weather: %w", err)
	}

	latency := time.Since(startTime).Milliseconds()
	s.logger.Infof("[Weather] [OpenMeteo] Complete: city=%s, temp=%.1f°C, weather=%s, latency=%dms", city, weather.TemperatureC, weather.Weather, latency)

	return weather, nil
}

func (s *openMeteoService) geocode(ctx context.Context, city string) (float64, float64, error) {
	u := fmt.Sprintf("https://geocoding-api.open-meteo.com/v1/search?name=%s&count=1&language=zh&format=json", url.QueryEscape(city))

	var lastErr error
	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		s.logger.Infof("[Weather] [OpenMeteo] [Geocode] Attempt %d/%d: city=%s", attempt, s.maxRetries, city)

		lat, lon, err := s.doGeocode(ctx, u)
		if err == nil {
			s.logger.Infof("[Weather] [OpenMeteo] [Geocode] Success: city=%s, lat=%.4f, lon=%.4f", city, lat, lon)
			return lat, lon, nil
		}

		lastErr = err
		s.logger.Errorf("[Weather] [OpenMeteo] [Geocode] Attempt %d failed: city=%s, error=%v", attempt, city, err)
		if attempt < s.maxRetries {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}

	return 0, 0, fmt.Errorf("geocoding failed after %d attempts for city %s: %w", s.maxRetries, city, lastErr)
}

func (s *openMeteoService) doGeocode(ctx context.Context, u string) (float64, float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("geocoding API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read response body: %w", err)
	}

	var result struct {
		Results []struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, 0, fmt.Errorf("failed to parse JSON: %w", err)
	}

	if len(result.Results) == 0 {
		return 0, 0, ErrCityNotFound
	}

	return result.Results[0].Latitude, result.Results[0].Longitude, nil
}

func (s *openMeteoService) queryWeather(ctx context.Context, lat, lon float64) (*WeatherData, error) {
	u := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,weather_code,wind_speed_10m&timezone=auto",
		lat, lon,
	)

	var lastErr error
	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		s.logger.Infof("[Weather] [OpenMeteo] [Query] Attempt %d/%d: lat=%.4f, lon=%.4f", attempt, s.maxRetries, lat, lon)

		weather, err := s.doQueryWeather(ctx, u)
		if err == nil {
			s.logger.Infof("[Weather] [OpenMeteo] [Query] Success: lat=%.4f, lon=%.4f, temp=%.1f°C, weather=%s", lat, lon, weather.TemperatureC, weather.Weather)
			return weather, nil
		}

		lastErr = err
		s.logger.Errorf("[Weather] [OpenMeteo] [Query] Attempt %d failed: lat=%.4f, lon=%.4f, error=%v", attempt, lat, lon, err)
		if attempt < s.maxRetries {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}

	return nil, fmt.Errorf("weather query failed after %d attempts for lat=%.4f, lon=%.4f: %w", s.maxRetries, lat, lon, lastErr)
}

func (s *openMeteoService) doQueryWeather(ctx context.Context, u string) (*WeatherData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weather API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var result struct {
		Current struct {
			Time        string  `json:"time"`
			Temperature float64 `json:"temperature_2m"`
			WeatherCode int     `json:"weather_code"`
			WindSpeed   float64 `json:"wind_speed_10m"`
		} `json:"current"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &WeatherData{
		City:         "",
		Time:         result.Current.Time,
		TemperatureC: result.Current.Temperature,
		Weather:      weatherCodeToDesc(result.Current.WeatherCode),
		WindSpeedKmh: result.Current.WindSpeed,
	}, nil
}

func weatherCodeToDesc(code int) string {
	switch code {
	case 0:
		return "晴朗"
	case 1, 2, 3:
		return "多云"
	case 45, 48:
		return "雾"
	case 51, 53, 55:
		return "毛毛雨"
	case 56, 57:
		return "冻雨"
	case 61, 63, 65:
		return "下雨"
	case 66, 67:
		return "雨夹雪"
	case 71, 73, 75:
		return "下雪"
	case 77:
		return "雪粒"
	case 80, 81, 82:
		return "阵雨"
	case 85, 86:
		return "阵雪"
	case 95:
		return "雷雨"
	case 96, 99:
		return "雷雨伴冰雹"
	default:
		return "未知天气"
	}
}
