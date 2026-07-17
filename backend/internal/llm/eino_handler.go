package llm

import (
	"context"
	"fmt"
	"time"

	eino_callbacks "github.com/cloudwego/eino/callbacks"
	eino_components "github.com/cloudwego/eino/components"
	eino_compose "github.com/cloudwego/eino/compose"
	eino_model "github.com/cloudwego/eino/components/model"
	eino_schema "github.com/cloudwego/eino/schema"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/pkg/logging"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type callCounterKey struct{}

type chatModelStartInfo struct {
	startTime time.Time
	input     eino_callbacks.CallbackInput
}

type einoAgentHandler struct {
	logger        logging.Logger
	toolsUsed     []string
	modelStarts   map[int]chatModelStartInfo
}

func newEinoAgentHandler(logger logging.Logger) eino_callbacks.Handler {
	return &einoAgentHandler{
		logger:      logger,
		modelStarts: make(map[int]chatModelStartInfo),
	}
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
		h.modelStarts[callCounter] = chatModelStartInfo{
			startTime: time.Now(),
			input:     input,
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

	if info.Component == eino_components.ComponentOfTool {
		h.logger.Info("[Audit] Tool call completed",
			"tool_name", info.Name,
			"output", h.formatToolOutput(output),
		)
	}

	if info.Component == eino_components.ComponentOfChatModel {
		h.recordLangfuseGeneration(ctx, info, output)
	}

	return ctx
}

func (h *einoAgentHandler) recordLangfuseGeneration(ctx context.Context, info *eino_callbacks.RunInfo, output eino_callbacks.CallbackOutput) {
	trace := observability.GetLLMTraceFromContext(ctx)
	if trace == nil || !trace.Enabled() {
		return
	}

	var callCounter int
	if val := ctx.Value(callCounterKey{}); val != nil {
		callCounter = val.(int)
	}

	startInfo, ok := h.modelStarts[callCounter]
	if !ok {
		return
	}
	delete(h.modelStarts, callCounter)

	latencyMs := time.Since(startInfo.startTime).Milliseconds()

	modelCallbackOutput := eino_model.ConvCallbackOutput(output)
	if modelCallbackOutput == nil {
		return
	}

	modelName := info.Name
	if modelCallbackOutput.Config != nil && modelCallbackOutput.Config.Model != "" {
		modelName = modelCallbackOutput.Config.Model
	}

	inputMessages := h.extractInputMessages(startInfo.input)

	outputContent := ""
	if modelCallbackOutput.Message != nil {
		outputContent = modelCallbackOutput.Message.Content
	}

	inputTokens := 0
	outputTokens := 0
	if modelCallbackOutput.TokenUsage != nil {
		inputTokens = modelCallbackOutput.TokenUsage.PromptTokens
		outputTokens = modelCallbackOutput.TokenUsage.CompletionTokens
	}

	purpose := "tool_decision"
	if callCounter > 1 {
		purpose = "final_response"
	}

	trace.RecordGeneration(
		fmt.Sprintf("chat-model-%d-%s", callCounter, purpose),
		modelName,
		inputMessages,
		outputContent,
		inputTokens,
		outputTokens,
		0,
		latencyMs,
		nil,
	)

	h.logger.Info("[Langfuse] Auto-recorded generation",
		"call_number", callCounter,
		"model", modelName,
		"input_tokens", inputTokens,
		"output_tokens", outputTokens,
		"latency_ms", latencyMs,
	)
}

func (h *einoAgentHandler) extractInputMessages(input eino_callbacks.CallbackInput) interface{} {
	modelInput := eino_model.ConvCallbackInput(input)
	if modelInput == nil {
		return input
	}

	if modelInput.Messages != nil {
		return modelInput.Messages
	}

	return input
}

func (h *einoAgentHandler) OnError(ctx context.Context, info *eino_callbacks.RunInfo, err error) context.Context {
	span := trace.SpanFromContext(ctx)
	if span != nil && span.IsRecording() {
		span.RecordError(err)
		span.End()
	}

	h.logger.Error("[Audit] Component error",
		"component", info.Component,
		"name", info.Name,
		"error", err.Error(),
	)

	if info.Component == eino_components.ComponentOfChatModel {
		h.recordLangfuseGenerationError(ctx, info, err)
	}

	return ctx
}

func (h *einoAgentHandler) recordLangfuseGenerationError(ctx context.Context, info *eino_callbacks.RunInfo, err error) {
	trace := observability.GetLLMTraceFromContext(ctx)
	if trace == nil || !trace.Enabled() {
		return
	}

	var callCounter int
	if val := ctx.Value(callCounterKey{}); val != nil {
		callCounter = val.(int)
	}

	startInfo, ok := h.modelStarts[callCounter]
	if !ok {
		return
	}
	delete(h.modelStarts, callCounter)

	latencyMs := time.Since(startInfo.startTime).Milliseconds()

	purpose := "tool_decision"
	if callCounter > 1 {
		purpose = "final_response"
	}

	trace.RecordGeneration(
		fmt.Sprintf("chat-model-%d-%s", callCounter, purpose),
		info.Name,
		startInfo.input,
		"",
		0,
		0,
		0,
		latencyMs,
		err,
	)

	h.logger.Info("[Langfuse] Auto-recorded generation error",
		"call_number", callCounter,
		"model", info.Name,
		"latency_ms", latencyMs,
		"error", err.Error(),
	)
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
		h.modelStarts[callCounter] = chatModelStartInfo{
			startTime: time.Now(),
			input:     nil,
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

	if info.Component == eino_components.ComponentOfTool {
		h.logger.Info("[Audit] Tool stream completed",
			"tool_name", info.Name,
		)
	}

	if info.Component == eino_components.ComponentOfChatModel {
		h.recordLangfuseGenerationFromStream(ctx, info, output)
	}

	return ctx
}

func (h *einoAgentHandler) recordLangfuseGenerationFromStream(ctx context.Context, info *eino_callbacks.RunInfo, output *eino_schema.StreamReader[eino_callbacks.CallbackOutput]) {
	trace := observability.GetLLMTraceFromContext(ctx)
	if trace == nil || !trace.Enabled() {
		return
	}

	var callCounter int
	if val := ctx.Value(callCounterKey{}); val != nil {
		callCounter = val.(int)
	}

	startInfo, ok := h.modelStarts[callCounter]
	if !ok {
		return
	}
	delete(h.modelStarts, callCounter)

	latencyMs := time.Since(startInfo.startTime).Milliseconds()

	purpose := "tool_decision"
	if callCounter > 1 {
		purpose = "final_response"
	}

	var fullOutput string
	var inputTokens, outputTokens int
	var modelName string = info.Name

	for {
		out, err := output.Recv()
		if err != nil {
			break
		}

		modelOutput := eino_model.ConvCallbackOutput(out)
		if modelOutput == nil {
			continue
		}

		if modelOutput.Message != nil && modelOutput.Message.Content != "" {
			fullOutput += modelOutput.Message.Content
		}

		if modelOutput.Config != nil && modelOutput.Config.Model != "" {
			modelName = modelOutput.Config.Model
		}

		if modelOutput.TokenUsage != nil {
			inputTokens = modelOutput.TokenUsage.PromptTokens
			outputTokens = modelOutput.TokenUsage.CompletionTokens
		}
	}

	trace.RecordGeneration(
		fmt.Sprintf("chat-model-%d-%s-stream", callCounter, purpose),
		modelName,
		"stream-input",
		fullOutput,
		inputTokens,
		outputTokens,
		0,
		latencyMs,
		nil,
	)

	h.logger.Info("[Langfuse] Auto-recorded stream generation",
		"call_number", callCounter,
		"model", modelName,
		"output_len", len(fullOutput),
		"input_tokens", inputTokens,
		"output_tokens", outputTokens,
		"latency_ms", latencyMs,
	)
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
