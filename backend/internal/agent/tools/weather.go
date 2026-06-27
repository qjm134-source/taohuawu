package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	eino_tool "github.com/cloudwego/eino/components/tool"
	eino_tool_utils "github.com/cloudwego/eino/components/tool/utils"
)

const weatherAPITimeout = 15 * time.Second

// 重试配置
const (
	maxRetries = 2
)

const defaultCity = "杭州"

var waterTownKeywords = []string{"水乡", "江南", "这里", "此地", "当地", "本地", "景区", "古镇"}

type GetWeatherInput struct {
	City string `json:"city" jsonschema:"required" jsonschema_description:"城市名称，例如 苏州、上海、杭州"`
}

type GetWeatherOutput struct {
	City         string  `json:"city"`
	Time         string  `json:"time"`
	TemperatureC float64 `json:"temperature_c"`
	Weather      string  `json:"weather"`
	WindSpeedKmh float64 `json:"wind_speed_kmh"`
}

type getWeatherToolImpl struct {
	client *http.Client
}

func (t *getWeatherToolImpl) invoke(ctx context.Context, input GetWeatherInput) (GetWeatherOutput, error) {
	city := t.normalizeCity(input.City)

	lat, lon, err := t.geocode(ctx, city)
	if err != nil {
		return GetWeatherOutput{}, fmt.Errorf("failed to geocode city %s: %w", city, err)
	}

	weather, err := t.queryWeather(ctx, lat, lon)
	if err != nil {
		return GetWeatherOutput{}, fmt.Errorf("failed to query weather: %w", err)
	}

	return GetWeatherOutput{
		City:         city,
		Time:         weather.Time,
		TemperatureC: weather.Temperature,
		Weather:      weather.WeatherDesc,
		WindSpeedKmh: weather.WindSpeed,
	}, nil
}

func (t *getWeatherToolImpl) normalizeCity(city string) string {
	city = strings.TrimSpace(city)
	if city == "" {
		return defaultCity
	}

	lower := strings.ToLower(city)
	for _, kw := range waterTownKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return defaultCity
		}
	}

	return city
}

func (t *getWeatherToolImpl) geocode(ctx context.Context, city string) (float64, float64, error) {
	u := fmt.Sprintf("https://geocoding-api.open-meteo.com/v1/search?name=%s&count=1&language=zh&format=json", url.QueryEscape(city))

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.Printf("[Weather] [Geocode] Attempt %d/%d: city=%s, url=%s", attempt, maxRetries, city, u)

		lat, lon, err := t.doGeocode(ctx, u, city, attempt)
		if err == nil {
			log.Printf("[Weather] [Geocode] Success: city=%s, lat=%.4f, lon=%.4f", city, lat, lon)
			return lat, lon, nil
		}

		lastErr = err
		log.Printf("[Weather] [Geocode] Attempt %d failed: city=%s, error=%v", attempt, city, err)
	}

	return 0, 0, fmt.Errorf("geocoding failed after %d attempts for city %s: %w", maxRetries, city, lastErr)
}

func (t *getWeatherToolImpl) doGeocode(ctx context.Context, u, city string, attempt int) (float64, float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("request failed (attempt %d): %w", attempt, err)
	}
	defer resp.Body.Close()

	log.Printf("[Weather] [Geocode] Response: city=%s, status=%d, status_text=%s", city, resp.StatusCode, resp.Status)

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("geocoding API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read response body: %w", err)
	}

	log.Printf("[Weather] [Geocode] Response body length: %d bytes", len(body))

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
		return 0, 0, fmt.Errorf("city not found: %s", city)
	}

	return result.Results[0].Latitude, result.Results[0].Longitude, nil
}

type weatherData struct {
	Time        string
	Temperature float64
	WeatherDesc string
	WindSpeed   float64
}

func (t *getWeatherToolImpl) queryWeather(ctx context.Context, lat, lon float64) (*weatherData, error) {
	u := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,weather_code,wind_speed_10m&timezone=auto",
		lat, lon,
	)

	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.Printf("[Weather] [Query] Attempt %d/%d: lat=%.4f, lon=%.4f, url=%s", attempt, maxRetries, lat, lon, u)

		weather, err := t.doQueryWeather(ctx, u, lat, lon, attempt)
		if err == nil {
			log.Printf("[Weather] [Query] Success: lat=%.4f, lon=%.4f, temp=%.1f°C, weather=%s, wind=%.1fkm/h",
				lat, lon, weather.Temperature, weather.WeatherDesc, weather.WindSpeed)
			return weather, nil
		}

		lastErr = err
		log.Printf("[Weather] [Query] Attempt %d failed: lat=%.4f, lon=%.4f, error=%v", attempt, lat, lon, err)
	}

	return nil, fmt.Errorf("weather query failed after %d attempts for lat=%.4f, lon=%.4f: %w", maxRetries, lat, lon, lastErr)
}

func (t *getWeatherToolImpl) doQueryWeather(ctx context.Context, u string, lat, lon float64, attempt int) (*weatherData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed (attempt %d): %w", attempt, err)
	}
	defer resp.Body.Close()

	log.Printf("[Weather] [Query] Response: lat=%.4f, lon=%.4f, status=%d, status_text=%s", lat, lon, resp.StatusCode, resp.Status)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weather API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	log.Printf("[Weather] [Query] Response body length: %d bytes", len(body))

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

	return &weatherData{
		Time:        result.Current.Time,
		Temperature: result.Current.Temperature,
		WeatherDesc: weatherCodeToDesc(result.Current.WeatherCode),
		WindSpeed:   result.Current.WindSpeed,
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

func NewGetWeatherTool() eino_tool.InvokableTool {
	impl := &getWeatherToolImpl{
		client: &http.Client{Timeout: weatherAPITimeout},
	}
	tool, err := eino_tool_utils.InferTool[GetWeatherInput, GetWeatherOutput](
		"get_weather",
		"查询指定城市的实时天气，包括温度、天气状况和风力。当玩家询问天气时调用。如果玩家未指定城市或询问水乡/当地天气，默认查询杭州。",
		impl.invoke,
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create get_weather tool: %v", err))
	}
	return tool
}
