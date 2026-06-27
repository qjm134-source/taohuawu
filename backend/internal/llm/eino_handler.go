package llm

import (
	"context"
	"fmt"
	"time"

	eino_callbacks "github.com/cloudwego/eino/callbacks"
	eino_components "github.com/cloudwego/eino/components"
	eino_schema "github.com/cloudwego/eino/schema"
	"github.com/watertown/guide/pkg/logging"
)

type einoAgentHandler struct {
	logger logging.Logger
}

func newEinoAgentHandler(logger logging.Logger) eino_callbacks.Handler {
	return &einoAgentHandler{logger: logger}
}

func (h *einoAgentHandler) OnStart(ctx context.Context, info *eino_callbacks.RunInfo, input eino_callbacks.CallbackInput) context.Context {
	startTime := time.Now()
	ctx = context.WithValue(ctx, "startTime", startTime)

	h.logger.Info("[Trace] OnStart",
		"name", info.Name,
		"type", info.Type,
		"component", info.Component,
	)

	// 根据组件类型记录不同的审计日志
	switch info.Component {
	case eino_components.ComponentOfChatModel:
		h.logger.Info("[Audit] Model call started",
			"model_name", info.Name,
			"type", info.Type,
			"input_preview", h.previewInput(input),
		)
	case eino_components.ComponentOfTool:
		h.logger.Info("[Audit] Tool call started",
			"tool_name", info.Name,
			"input", h.formatToolInput(input),
		)
	}

	return ctx
}

func (h *einoAgentHandler) OnEnd(ctx context.Context, info *eino_callbacks.RunInfo, output eino_callbacks.CallbackOutput) context.Context {
	startTime, ok := ctx.Value("startTime").(time.Time)
	latency := time.Duration(0)
	if ok {
		latency = time.Since(startTime)
	}

	h.logger.Info("[Trace] OnEnd",
		"name", info.Name,
		"type", info.Type,
		"component", info.Component,
		"latency_ms", latency.Milliseconds(),
	)

	// 根据组件类型记录不同的审计日志
	switch info.Component {
	case eino_components.ComponentOfChatModel:
		h.logger.Info("[Audit] Model call completed",
			"model_name", info.Name,
			"latency_ms", latency.Milliseconds(),
			"output_preview", h.previewOutput(output),
		)
	case eino_components.ComponentOfTool:
		h.logger.Info("[Audit] Tool call completed",
			"tool_name", info.Name,
			"latency_ms", latency.Milliseconds(),
			"output", h.formatToolOutput(output),
		)
	}

	return ctx
}

func (h *einoAgentHandler) OnError(ctx context.Context, info *eino_callbacks.RunInfo, err error) context.Context {
	startTime, ok := ctx.Value("startTime").(time.Time)
	latency := time.Duration(0)
	if ok {
		latency = time.Since(startTime)
	}

	h.logger.Error("[Trace] OnError",
		"name", info.Name,
		"type", info.Type,
		"component", info.Component,
		"error", err.Error(),
		"latency_ms", latency.Milliseconds(),
	)

	// 审计日志记录错误
	h.logger.Error("[Audit] Component error",
		"component", info.Component,
		"name", info.Name,
		"error", err.Error(),
		"latency_ms", latency.Milliseconds(),
	)

	return ctx
}

func (h *einoAgentHandler) OnStartWithStreamInput(ctx context.Context, info *eino_callbacks.RunInfo, input *eino_schema.StreamReader[eino_callbacks.CallbackInput]) context.Context {
	startTime := time.Now()
	ctx = context.WithValue(ctx, "startTime", startTime)

	h.logger.Info("[Trace] Stream start",
		"name", info.Name,
		"type", info.Type,
		"component", info.Component,
	)

	h.logger.Info("[Audit] Stream call started",
		"component", info.Component,
		"name", info.Name,
	)

	return ctx
}

func (h *einoAgentHandler) OnEndWithStreamOutput(ctx context.Context, info *eino_callbacks.RunInfo, output *eino_schema.StreamReader[eino_callbacks.CallbackOutput]) context.Context {
	startTime, ok := ctx.Value("startTime").(time.Time)
	latency := time.Duration(0)
	if ok {
		latency = time.Since(startTime)
	}

	h.logger.Info("[Trace] Stream end",
		"name", info.Name,
		"type", info.Type,
		"component", info.Component,
		"latency_ms", latency.Milliseconds(),
	)

	h.logger.Info("[Audit] Stream call completed",
		"component", info.Component,
		"name", info.Name,
		"latency_ms", latency.Milliseconds(),
	)

	return ctx
}

// 辅助方法：预览输入（限制长度）
func (h *einoAgentHandler) previewInput(input eino_callbacks.CallbackInput) string {
	if input == nil {
		return "nil"
	}
	str := fmt.Sprintf("%v", input)
	if len(str) > 200 {
		return str[:200] + "..."
	}
	return str
}

// 辅助方法：预览输出（限制长度）
func (h *einoAgentHandler) previewOutput(output eino_callbacks.CallbackOutput) string {
	if output == nil {
		return "nil"
	}
	str := fmt.Sprintf("%v", output)
	if len(str) > 200 {
		return str[:200] + "..."
	}
	return str
}

// 辅助方法：格式化工具输入
func (h *einoAgentHandler) formatToolInput(input eino_callbacks.CallbackInput) string {
	if input == nil {
		return "nil"
	}
	// 尝试转换为字符串
	str := fmt.Sprintf("%v", input)
	if len(str) > 500 {
		return str[:500] + "..."
	}
	return str
}

// 辅助方法：格式化工具输出
func (h *einoAgentHandler) formatToolOutput(output eino_callbacks.CallbackOutput) string {
	if output == nil {
		return "nil"
	}
	str := fmt.Sprintf("%v", output)
	if len(str) > 500 {
		return str[:500] + "..."
	}
	return str
}
