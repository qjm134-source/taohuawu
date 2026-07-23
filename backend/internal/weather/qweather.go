package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/watertown/guide/pkg/logging"
)

type qWeatherService struct {
	apiKey     string
	baseURL    string
	client     *http.Client
	maxRetries int
	logger     logging.Logger
}

func NewQWeatherService(cfg QWeatherConfig, logger logging.Logger) Service {
	return &qWeatherService{
		apiKey:     cfg.APIKey,
		baseURL:    cfg.BaseURL,
		maxRetries: cfg.MaxRetries,
		logger:     logger,
		client:     newWeatherHTTPClient(cfg.Timeout),
	}
}

func (s *qWeatherService) GetWeather(ctx context.Context, city string) (*WeatherData, error) {
	startTime := time.Now()
	s.logger.Infof("[Weather] [QWeather] Start: city=%s", city)

	locationID, err := s.searchLocation(ctx, city)
	if err != nil {
		s.logger.Errorf("[Weather] [QWeather] Failed: city=%s, latency=%dms, error=%v", city, time.Since(startTime).Milliseconds(), err)
		return nil, fmt.Errorf("failed to search location for %s: %w", city, err)
	}

	weather, err := s.getCurrentWeather(ctx, locationID)
	if err != nil {
		s.logger.Errorf("[Weather] [QWeather] Failed: city=%s, latency=%dms, error=%v", city, time.Since(startTime).Milliseconds(), err)
		return nil, fmt.Errorf("failed to get weather for %s: %w", city, err)
	}

	latency := time.Since(startTime).Milliseconds()
	s.logger.Infof("[Weather] [QWeather] Complete: city=%s, temp=%.1f°C, weather=%s, latency=%dms", city, weather.TemperatureC, weather.Weather, latency)

	return weather, nil
}

func (s *qWeatherService) searchLocation(ctx context.Context, city string) (string, error) {
	u := fmt.Sprintf("%s/v7/geo/city?key=%s&location=%s", s.baseURL, s.apiKey, url.QueryEscape(city))

	var lastErr error
	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		s.logger.Infof("[Weather] [QWeather] [Search] Attempt %d/%d: city=%s", attempt, s.maxRetries, city)

		locationID, err := s.doSearchLocation(ctx, u)
		if err == nil {
			s.logger.Infof("[Weather] [QWeather] [Search] Success: city=%s, location_id=%s", city, locationID)
			return locationID, nil
		}

		lastErr = err
		s.logger.Errorf("[Weather] [QWeather] [Search] Attempt %d failed: city=%s, error=%v", attempt, city, err)
		if attempt < s.maxRetries {
			time.Sleep(retryDelay(attempt))
		}
	}

	return "", fmt.Errorf("search failed after %d attempts for city %s: %w", s.maxRetries, city, lastErr)
}

func (s *qWeatherService) doSearchLocation(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	s.logger.Infof("[Weather] [QWeather] [Search] Response: status=%d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		Code     string `json:"code"`
		Location []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"location"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse JSON: %w", err)
	}

	if result.Code != "200" {
		return "", fmt.Errorf("API returned code %s", result.Code)
	}

	if len(result.Location) == 0 {
		return "", ErrCityNotFound
	}

	return result.Location[0].ID, nil
}

func (s *qWeatherService) getCurrentWeather(ctx context.Context, locationID string) (*WeatherData, error) {
	u := fmt.Sprintf("%s/v7/weather/now?key=%s&location=%s", s.baseURL, s.apiKey, locationID)

	var lastErr error
	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		s.logger.Infof("[Weather] [QWeather] [Now] Attempt %d/%d: location_id=%s", attempt, s.maxRetries, locationID)

		weather, err := s.doGetCurrentWeather(ctx, u)
		if err == nil {
			s.logger.Infof("[Weather] [QWeather] [Now] Success: location_id=%s, temp=%.1f°C, weather=%s", locationID, weather.TemperatureC, weather.Weather)
			return weather, nil
		}

		lastErr = err
		s.logger.Errorf("[Weather] [QWeather] [Now] Attempt %d failed: location_id=%s, error=%v", attempt, locationID, err)
		if attempt < s.maxRetries {
			time.Sleep(retryDelay(attempt))
		}
	}

	return nil, fmt.Errorf("weather query failed after %d attempts for location %s: %w", s.maxRetries, locationID, lastErr)
}

func (s *qWeatherService) doGetCurrentWeather(ctx context.Context, u string) (*WeatherData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	s.logger.Infof("[Weather] [QWeather] [Now] Response: status=%d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		Code string `json:"code"`
		Now  struct {
			Temp      string `json:"temp"`
			Text      string `json:"text"`
			WindSpeed string `json:"windSpeed"`
			ObsTime   string `json:"obsTime"`
		} `json:"now"`
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	if result.Code != "200" {
		return nil, fmt.Errorf("API returned code %s", result.Code)
	}

	temp, err := parseFloat(result.Now.Temp)
	if err != nil {
		return nil, fmt.Errorf("invalid temperature: %w", err)
	}

	windSpeed, err := parseFloat(result.Now.WindSpeed)
	if err != nil {
		windSpeed = 0
	}

	return &WeatherData{
		City:         result.Location.Name,
		Time:         formatTime(result.Now.ObsTime),
		TemperatureC: temp,
		Weather:      result.Now.Text,
		WindSpeedKmh: windSpeed,
	}, nil
}

func parseFloat(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

func formatTime(t string) string {
	if strings.Contains(t, "T") {
		parts := strings.Split(t, "T")
		if len(parts) == 2 {
			return parts[1][:5]
		}
	}
	return t
}
