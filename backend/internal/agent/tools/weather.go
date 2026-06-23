package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const weatherAPITimeout = 15 * time.Second // 增加到 15 秒，适应网络波动

// defaultCity 当玩家未指定城市或询问水乡/当地天气时，默认查询杭州。
const defaultCity = "杭州"

// waterTownKeywords 表示玩家询问的是水乡/江南/当地天气的关键词。
var waterTownKeywords = []string{"水乡", "江南", "这里", "此地", "当地", "本地", "景区", "古镇"}

// GetWeatherTool 查询实时天气的工具。
// 使用 Open-Meteo 免费天气 API，无需 API Key，适合演示与测试。
type GetWeatherTool struct {
	client *http.Client
}

// NewGetWeatherTool 创建天气工具实例。
func NewGetWeatherTool() *GetWeatherTool {
	return &GetWeatherTool{
		client: &http.Client{Timeout: weatherAPITimeout},
	}
}

func (t *GetWeatherTool) Name() string {
	return "get_weather"
}

func (t *GetWeatherTool) Description() string {
	return "查询指定城市的实时天气，包括温度、天气状况和风力。当玩家询问天气时调用。如果玩家未指定城市或询问水乡/当地天气，默认查询杭州。"
}

func (t *GetWeatherTool) ParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"city": map[string]interface{}{
				"type":        "string",
				"description": "城市名称，例如 苏州、上海、杭州",
			},
		},
		"required": []string{"city"},
	}
}

func (t *GetWeatherTool) Timeout() time.Duration {
	return weatherAPITimeout
}

func (t *GetWeatherTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	city := t.normalizeCity(params)

	lat, lon, err := t.geocode(ctx, city)
	if err != nil {
		return nil, fmt.Errorf("failed to geocode city %s: %w", city, err)
	}

	weather, err := t.queryWeather(ctx, lat, lon)
	if err != nil {
		return nil, fmt.Errorf("failed to query weather: %w", err)
	}

	return map[string]interface{}{
		"city":           city,
		"time":           weather.Time,
		"temperature_c":  weather.Temperature,
		"weather":        weather.WeatherDesc,
		"wind_speed_kmh": weather.WindSpeed,
	}, nil
}

// normalizeCity 解析并规范化城市参数。
// 如果玩家未指定城市，或询问的是水乡/当地天气，则默认返回杭州。
func (t *GetWeatherTool) normalizeCity(params map[string]interface{}) string {
	raw, ok := params["city"].(string)
	if !ok {
		return defaultCity
	}

	city := strings.TrimSpace(raw)
	if city == "" {
		return defaultCity
	}

	// 如果玩家询问的是水乡/江南/当地天气，默认查询杭州
	lower := strings.ToLower(city)
	for _, kw := range waterTownKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return defaultCity
		}
	}

	return city
}

// geocode 使用 Open-Meteo Geocoding API 将城市名转换为经纬度。
func (t *GetWeatherTool) geocode(ctx context.Context, city string) (float64, float64, error) {
	u := fmt.Sprintf("https://geocoding-api.open-meteo.com/v1/search?name=%s&count=1&language=zh&format=json", url.QueryEscape(city))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("geocoding API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}

	var result struct {
		Results []struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, 0, err
	}

	if len(result.Results) == 0 {
		return 0, 0, fmt.Errorf("city not found: %s", city)
	}

	return result.Results[0].Latitude, result.Results[0].Longitude, nil
}

// weatherData 保存从 Open-Meteo 解析出的天气数据。
type weatherData struct {
	Time        string
	Temperature float64
	WeatherDesc string
	WindSpeed   float64
}

// queryWeather 使用 Open-Meteo Weather API 查询当前天气。
func (t *GetWeatherTool) queryWeather(ctx context.Context, lat, lon float64) (*weatherData, error) {
	u := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,weather_code,wind_speed_10m&timezone=auto",
		lat, lon,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weather API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	return &weatherData{
		Time:        result.Current.Time,
		Temperature: result.Current.Temperature,
		WeatherDesc: weatherCodeToDesc(result.Current.WeatherCode),
		WindSpeed:   result.Current.WindSpeed,
	}, nil
}

// weatherCodeToDesc 将 Open-Meteo WMO weather code 转换为中文描述。
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
