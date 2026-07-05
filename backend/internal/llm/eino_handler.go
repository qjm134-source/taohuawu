package llm

import (
	"context"
	"fmt"

	eino_callbacks "github.com/cloudwego/eino/callbacks"
	eino_components "github.com/cloudwego/eino/components"
	eino_compose "github.com/cloudwego/eino/compose"
	eino_schema "github.com/cloudwego/eino/schema"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/pkg/logging"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type callCounterKey struct{}

type einoAgentHandler struct {
	logger    logging.Logger
	toolsUsed []string
}

func newEinoAgentHandler(logger logging.Logger) eino_callbacks.Handler {
	return &einoAgentHandler{logger: logger}
}

func (h *einoAgentHandler) GetToolsUsed() []string {
	return h.toolsUsed
}

func (h *einoAgentHandler) OnStart(ctx context.Context, info *eino_callbacks.RunInfo, input eino_callbacks.CallbackInput) context.Context {
	var callCounter int
	if val := ctx.Value(callCounterKey{}); val != nil {
		callCounter = val.(int)
	}
	callCounter++
	ctx = context.WithValue(ctx, callCounterKey{}, callCounter)

	h.logger.Info("[EinoCallback] OnStart called", "component", info.Component, "name", info.Name, "type", info.Type)

	spanName := fmt.Sprintf("Eino.%s.%s", info.Component, info.Name)
	purpose := ""
	if info.Component == eino_components.ComponentOfChatModel {
		spanName = fmt.Sprintf("Eino.%s.%s.%d", info.Component, info.Name, callCounter)
		if callCounter == 1 {
			purpose = "tool_decision"
		} else {
			purpose = "final_response"
		}
	} else if info.Component == eino_components.ComponentOfTool {
		spanName = fmt.Sprintf("Eino.%s.%s.%d", info.Component, info.Name, callCounter)
		purpose = "tool_execution"
	} else if info.Component == eino_compose.ComponentOfLambda {
		purpose = "state_management"
	} else if info.Component == eino_compose.ComponentOfAgenticToolsNode || info.Component == eino_compose.ComponentOfToolsNode {
		purpose = "tool_execution"
	}

	ctx, _ = observability.StartChildSpan(ctx, spanName)
	if purpose != "" {
		span := trace.SpanFromContext(ctx)
		if span != nil && span.IsRecording() {
			span.SetAttributes(
				attribute.String("component", string(info.Component)),
				attribute.String("name", info.Name),
				attribute.String("type", info.Type),
				attribute.Int("call_number", callCounter),
				attribute.String("purpose", purpose),
			)
		}
	}

	// 只记录工具调用的审计日志
	if info.Component == eino_components.ComponentOfTool {
		h.logger.Info("[Audit] Tool call started",
			"tool_name", info.Name,
			"input", h.formatToolInput(input),
		)

		h.toolsUsed = append(h.toolsUsed, info.Name)
	}

	return ctx
}

func (h *einoAgentHandler) OnEnd(ctx context.Context, info *eino_callbacks.RunInfo, output eino_callbacks.CallbackOutput) context.Context {
	span := trace.SpanFromContext(ctx)
	if span != nil && span.IsRecording() {
		if info.Component == eino_compose.ComponentOfGraph {
			span.SetAttributes(
				attribute.String("note", "Child spans sum may differ due to framework overhead (state management, message routing, loop control)"),
				attribute.String("framework", "Eino ReAct Agent"),
			)
		}
		span.End()
	}

	// 只记录工具调用的审计日志
	if info.Component == eino_components.ComponentOfTool {
		h.logger.Info("[Audit] Tool call completed",
			"tool_name", info.Name,
			"output", h.formatToolOutput(output),
		)
	}

	return ctx
}

func (h *einoAgentHandler) OnError(ctx context.Context, info *eino_callbacks.RunInfo, err error) context.Context {
	span := trace.SpanFromContext(ctx)
	if span != nil && span.IsRecording() {
		span.RecordError(err)
		span.End()
	}

	// 记录错误日志
	h.logger.Error("[Audit] Component error",
		"component", info.Component,
		"name", info.Name,
		"error", err.Error(),
	)

	return ctx
}

func (h *einoAgentHandler) OnStartWithStreamInput(ctx context.Context, info *eino_callbacks.RunInfo, input *eino_schema.StreamReader[eino_callbacks.CallbackInput]) context.Context {
	var callCounter int
	if val := ctx.Value(callCounterKey{}); val != nil {
		callCounter = val.(int)
	}
	callCounter++
	ctx = context.WithValue(ctx, callCounterKey{}, callCounter)

	spanName := fmt.Sprintf("Eino.%s.%s", info.Component, info.Name)
	purpose := ""
	if info.Component == eino_components.ComponentOfChatModel {
		spanName = fmt.Sprintf("Eino.%s.%s.%d", info.Component, info.Name, callCounter)
		if callCounter == 1 {
			purpose = "tool_decision"
		} else {
			purpose = "final_response"
		}
	} else if info.Component == eino_components.ComponentOfTool {
		spanName = fmt.Sprintf("Eino.%s.%s.%d", info.Component, info.Name, callCounter)
		purpose = "tool_execution"
	} else if info.Component == eino_compose.ComponentOfLambda {
		purpose = "state_management"
	} else if info.Component == eino_compose.ComponentOfAgenticToolsNode || info.Component == eino_compose.ComponentOfToolsNode {
		purpose = "tool_execution"
	}

	ctx, _ = observability.StartChildSpan(ctx, spanName)
	if purpose != "" {
		span := trace.SpanFromContext(ctx)
		if span != nil && span.IsRecording() {
			span.SetAttributes(
				attribute.String("component", string(info.Component)),
				attribute.String("name", info.Name),
				attribute.String("type", info.Type),
				attribute.Int("call_number", callCounter),
				attribute.String("purpose", purpose),
			)
		}
	}

	// 只记录工具调用的审计日志
	if info.Component == eino_components.ComponentOfTool {
		h.logger.Info("[Audit] Tool stream started",
			"tool_name", info.Name,
		)

		h.toolsUsed = append(h.toolsUsed, info.Name)
	}

	return ctx
}

func (h *einoAgentHandler) OnEndWithStreamOutput(ctx context.Context, info *eino_callbacks.RunInfo, output *eino_schema.StreamReader[eino_callbacks.CallbackOutput]) context.Context {
	span := trace.SpanFromContext(ctx)
	if span != nil && span.IsRecording() {
		span.End()
	}

	// 只记录工具调用的审计日志
	if info.Component == eino_components.ComponentOfTool {
		h.logger.Info("[Audit] Tool stream completed",
			"tool_name", info.Name,
		)
	}

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
