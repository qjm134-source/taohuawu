package observability

import (
	"context"
	"encoding/base64"
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

type LangfuseConfig struct {
	Enabled   bool
	Host      string
	PublicKey string
	SecretKey string
}

type ObservabilityConfig struct {
	Enabled     bool
	ServiceName string
	Endpoint    string
	SampleRate  float64
	Exporter    string // "otlp"（默认，发送到 Jaeger/Collector）或 "stdout"（打印到标准输出）
	Langfuse    LangfuseConfig
}

// InitTracing 初始化追踪。
// exporter 支持两种模式：
//   - "otlp"：通过 OTLP/HTTP 将 Trace 发送到外部 Collector（如 Jaeger）
//   - "stdout"：将 Trace 以 JSON 格式打印到标准输出，适合开发调试
//
// 同时支持将 Trace 导出到 Langfuse（通过 OTLP）
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

	// 收集所有 exporters
	var exporters []sdktrace.SpanExporter

	// 根据配置选择主 exporter
	switch cfg.Exporter {
	case "stdout":
		exporter, err := stdouttrace.New(
			stdouttrace.WithWriter(os.Stdout),
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return nil, fmt.Errorf("create stdout exporter: %w", err)
		}
		exporters = append(exporters, exporter)
		fmt.Fprintln(os.Stderr, "[OTel] Using stdout exporter — traces will be printed to console/logs")

	default: // "otlp" 或空
		endpoint := cfg.Endpoint
		endpoint = strings.TrimPrefix(endpoint, "http://")
		endpoint = strings.TrimPrefix(endpoint, "https://")

		client := otlptracehttp.NewClient(
			otlptracehttp.WithInsecure(),
			otlptracehttp.WithEndpoint(endpoint),
		)
		exporter, err := otlptrace.New(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("create otlp exporter: %w", err)
		}
		exporters = append(exporters, exporter)
		fmt.Fprintf(os.Stderr, "[OTel] Using OTLP/HTTP exporter — sending traces to %s\n", cfg.Endpoint)
	}

	// 如果启用了 Langfuse，添加 Langfuse OTLP exporter
	if cfg.Langfuse.Enabled && cfg.Langfuse.PublicKey != "" && cfg.Langfuse.SecretKey != "" {
		langfuseEndpoint := cfg.Langfuse.Host
		if langfuseEndpoint == "" {
			langfuseEndpoint = "https://cloud.langfuse.com"
		}

		auth := base64.StdEncoding.EncodeToString([]byte(cfg.Langfuse.PublicKey + ":" + cfg.Langfuse.SecretKey))

		host := strings.TrimPrefix(langfuseEndpoint, "http://")
		host = strings.TrimPrefix(host, "https://")
		if !strings.Contains(host, ":") {
			host = host + ":4318"
		}

		langfuseClient := otlptracehttp.NewClient(
			otlptracehttp.WithEndpoint(host),
			otlptracehttp.WithURLPath("/api/public/otel/v1/traces"),
			otlptracehttp.WithInsecure(),
			otlptracehttp.WithHeaders(map[string]string{
				"Authorization":                "Basic " + auth,
				"x-langfuse-ingestion-version": "4",
			}),
		)

		langfuseExporter, err := otlptrace.New(ctx, langfuseClient)
		if err != nil {
			return nil, fmt.Errorf("create langfuse otlp exporter: %w", err)
		}
		exporters = append(exporters, &debugExporter{delegate: langfuseExporter, name: "langfuse"})
		fmt.Fprintf(os.Stderr, "[OTel] Using Langfuse OTLP exporter — sending traces to %s\n", langfuseEndpoint)
	}

	if len(exporters) == 0 {
		return nil, fmt.Errorf("no exporters configured")
	}

	// 创建 TracerProvider，支持多个 exporters
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	)

	// 使用 SimpleSpanProcessor 立即发送数据（便于调试）
	for _, exporter := range exporters {
		tp.RegisterSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter))
	}

	otel.SetTracerProvider(tp)

	return tp, nil
}

type debugExporter struct {
	delegate sdktrace.SpanExporter
	name     string
}

func (d *debugExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := d.delegate.ExportSpans(ctx, spans)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[OTel] [%s] Export failed: %v\n", d.name, err)
	}
	return err
}

func (d *debugExporter) Shutdown(ctx context.Context) error {
	return d.delegate.Shutdown(ctx)
}
