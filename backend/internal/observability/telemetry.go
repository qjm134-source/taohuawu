package observability

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

type ObservabilityConfig struct {
	Enabled     bool
	ServiceName string
	Endpoint    string
	SampleRate  float64
	Exporter    string // "otlp"（默认，发送到 Jaeger/Collector）或 "stdout"（打印到标准输出）
}

// InitTracing 初始化追踪。
// exporter 支持两种模式：
//   - "otlp"：通过 OTLP/HTTP 将 Trace 发送到外部 Collector（如 Jaeger）
//   - "stdout"：将 Trace 以 JSON 格式打印到标准输出，适合开发调试
func InitTracing(cfg ObservabilityConfig) (*sdktrace.TracerProvider, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	ctx := context.Background()

	// 创建资源
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.ServiceVersionKey.String("1.0.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	// 根据配置选择 exporter
	var exporter sdktrace.SpanExporter

	switch cfg.Exporter {
	case "stdout":
		exporter, err = stdouttrace.New(
			stdouttrace.WithWriter(os.Stdout),
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return nil, fmt.Errorf("create stdout exporter: %w", err)
		}
		fmt.Fprintln(os.Stderr, "[OTel] Using stdout exporter — traces will be printed to console/logs")

	default: // "otlp" 或空
		endpoint := cfg.Endpoint
		endpoint = strings.TrimPrefix(endpoint, "http://")
		endpoint = strings.TrimPrefix(endpoint, "https://")

		client := otlptracehttp.NewClient(
			otlptracehttp.WithInsecure(),
			otlptracehttp.WithEndpoint(endpoint),
		)
		exporter, err = otlptrace.New(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("create otlp exporter: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[OTel] Using OTLP/HTTP exporter — sending traces to %s\n", cfg.Endpoint)
	}

	// 创建 TracerProvider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	)

	otel.SetTracerProvider(tp)

	return tp, nil
}
