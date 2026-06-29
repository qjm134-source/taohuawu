package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	eino_tool "github.com/cloudwego/eino/components/tool"
	eino_tool_utils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/watertown/guide/internal/weather"
	"github.com/watertown/guide/pkg/logging"
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
	weatherService weather.Service
	logger         logging.Logger
}

func (t *getWeatherToolImpl) invoke(ctx context.Context, input GetWeatherInput) (GetWeatherOutput, error) {
	startTime := time.Now()
	city := t.normalizeCity(input.City)
	t.logger.Debugf("[Weather] [Invoke] Start: city=%s", city)

	weatherData, err := t.weatherService.GetWeather(ctx, city)
	if err != nil {
		t.logger.Errorf("[Weather] [Invoke] Failed: city=%s, latency=%dms, error=%v", city, time.Since(startTime).Milliseconds(), err)
		return GetWeatherOutput{}, fmt.Errorf("failed to get weather for %s: %w", city, err)
	}

	latency := time.Since(startTime).Milliseconds()
	t.logger.Debugf("[Weather] [Invoke] Complete: city=%s, temp=%.1f°C, weather=%s, latency=%dms", city, weatherData.TemperatureC, weatherData.Weather, latency)

	return GetWeatherOutput{
		City:         weatherData.City,
		Time:         weatherData.Time,
		TemperatureC: weatherData.TemperatureC,
		Weather:      weatherData.Weather,
		WindSpeedKmh: weatherData.WindSpeedKmh,
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

func NewGetWeatherTool(weatherService weather.Service, logger logging.Logger) eino_tool.InvokableTool {
	impl := &getWeatherToolImpl{
		weatherService: weatherService,
		logger:         logger,
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
