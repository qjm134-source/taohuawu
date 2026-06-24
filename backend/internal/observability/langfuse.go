package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/oiime/langfuse-go"
)

// LangfuseClient 封装 Langfuse SDK 客户端。
type LangfuseClient struct {
	client  *langfuse.Client
	enabled bool
}

// LangfuseConfig Langfuse 初始化配置。
// 与 config.LangfuseConfig 保持独立，避免循环依赖。
type LangfuseConfig struct {
	Enabled   bool
	Host      string
	PublicKey string
	SecretKey string
}

var globalLangfuse *LangfuseClient

// InitLangfuse 初始化全局 Langfuse 客户端。
// 若未启用或公钥为空，返回 nil（后续埋点调用会静默跳过）。
func InitLangfuse(cfg LangfuseConfig) *LangfuseClient {
	if !cfg.Enabled || cfg.PublicKey == "" || cfg.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "[Langfuse] Disabled — LLM traces will NOT be sent to Langfuse")
		globalLangfuse = &LangfuseClient{enabled: false}
		return globalLangfuse
	}

	host := cfg.Host
	if host == "" {
		host = "https://cloud.langfuse.com"
	}

	lf := langfuse.New(langfuse.Config{
		PublicKey:     cfg.PublicKey,
		SecretKey:     cfg.SecretKey,
		Host:          host,
		FlushInterval: 500 * time.Millisecond,
		FlushBatch:    100,
	})

	globalLangfuse = &LangfuseClient{client: lf, enabled: true}
	fmt.Fprintf(os.Stderr, "[Langfuse] Enabled — sending LLM traces to %s\n", host)
	return globalLangfuse
}

// GetLangfuse 获取全局 Langfuse 客户端，可能为 nil（未初始化或未启用）。
func GetLangfuse() *LangfuseClient {
	return globalLangfuse
}

// Enabled 返回是否可用。
func (c *LangfuseClient) Enabled() bool {
	return c != nil && c.enabled
}

// Shutdown 优雅关闭，刷新所有待发送事件。
func (c *LangfuseClient) Shutdown() {
	if c != nil && c.enabled {
		c.client.Shutdown(context.Background())
	}
}

// LLMTrace 封装一次 LLM 请求的 Langfuse Trace。
// 一个用户请求 = 一个 Trace，内部可能包含多次 LLM 调用（Generation）。
type LLMTrace struct {
	trace   *langfuse.Trace
	enabled bool
}

// StartLLMTrace 开始一次 Langfuse Trace。
// name: 如 "chat"、"welcome"、"chat-stream"
func StartLLMTrace(name, playerID, sessionID string) *LLMTrace {
	lf := GetLangfuse()
	if lf == nil || !lf.enabled {
		return &LLMTrace{enabled: false}
	}

	trace := lf.client.Trace(langfuse.TraceParams{
		Name:      name,
		UserID:    playerID,
		SessionID: sessionID,
		Metadata: map[string]any{
			"service": TracerName,
		},
	})

	return &LLMTrace{trace: trace, enabled: true}
}

// RecordGeneration 记录一次 LLM 调用（Generation）。
// 在一次 Trace 内可能调用多次（如主 LLM 失败后降级到 fallback）。
func (t *LLMTrace) RecordGeneration(name, model string,
	input interface{}, output string,
	inputTokens, outputTokens int,
	cost float64, latencyMs int64, err error) {

	if !t.enabled {
		return
	}

	inputJSON, _ := json.Marshal(input)
	usage := &langfuse.Usage{
		Input:  inputTokens,
		Output: outputTokens,
		Total:  inputTokens + outputTokens,
		Unit:   "TOKENS",
	}

	gen := t.trace.Generation(langfuse.GenerationParams{
		Name:            name,
		Model:           model,
		Input:           input,
		ModelParameters: map[string]string{},
	})

	update := langfuse.GenerationUpdate{
		Output: output,
		Usage:  usage,
	}

	if err != nil {
		update.StatusMessage = err.Error()
		update.Level = langfuse.ObservationLevelError
	}

	if cost > 0 {
		update.Metadata = map[string]any{
			"cost":          fmt.Sprintf("$%.6f", cost),
			"latency_ms":    latencyMs,
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"input_preview": truncateJSON(inputJSON, 200),
		}
	}

	gen.End(update)
}

// End 结束 Trace。
// Langfuse SDK 会自动处理 trace 的结束，无需显式调用。
func (t *LLMTrace) End() {
	// Langfuse SDK 会自动处理 trace 的结束
	// 如果需要更新 trace 元数据，可以使用 t.trace.Update()
}

// RecordScore 为 Trace 附加评分（如用户反馈）。
func (t *LLMTrace) RecordScore(name string, value float64, comment string) {
	if !t.enabled {
		return
	}
	t.trace.Score(langfuse.ScoreParams{
		Name:    name,
		Value:   value,
		Comment: comment,
	})
}

// truncateJSON 截断 JSON 字符串用于日志预览。
func truncateJSON(data []byte, maxLen int) string {
	s := string(data)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
