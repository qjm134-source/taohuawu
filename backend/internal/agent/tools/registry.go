// Package tools 提供 Agent 可调用的工具接口与注册表。
// 所有工具实现都放在本包内，按功能拆分到不同文件，避免 agent 根目录臃肿。
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/watertown/guide/internal/knowledge"
	"github.com/watertown/guide/internal/llm"
	"github.com/watertown/guide/pkg/utils"
)

// Tool 工具接口，所有可被 LLM 调用的工具都需要实现该接口。
type Tool interface {
	Name() string
	Description() string
	ParametersSchema() map[string]interface{} // 返回 JSON Schema 格式的参数定义，用于告诉 LLM 该工具接受哪些参数
	Execute(ctx context.Context, params map[string]interface{}) (interface{}, error)
	Timeout() time.Duration
}

// ToolRegistry 工具注册表
type ToolRegistry struct {
	tools map[string]Tool
}

// NewToolRegistry 创建工具注册表，并注册所有内置工具。
func NewToolRegistry(kb *knowledge.KnowledgeBase) *ToolRegistry {
	registry := &ToolRegistry{
		tools: make(map[string]Tool),
	}

	// 注册内置工具
	registry.Register(&GetPlayerInfoTool{})
	registry.Register(&GetGameGuideTool{KB: kb})
	registry.Register(&GetQuestInfoTool{KB: kb})
	registry.Register(&GetScenarioInfoTool{KB: kb})
	registry.Register(NewGetWeatherTool())

	return registry
}

// Register 注册工具
func (r *ToolRegistry) Register(tool Tool) {
	r.tools[tool.Name()] = tool
}

// Get 获取工具
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// List 列出所有工具
func (r *ToolRegistry) List() []Tool {
	result := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		result = append(result, tool)
	}
	return result
}

// Execute 执行工具
func (r *ToolRegistry) Execute(ctx context.Context, name string, params map[string]interface{}) (interface{}, error) {
	tool, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}

	ctx, cancel := utils.WithTimeoutFrom(ctx, tool.Timeout())
	defer cancel()

	select {
	case result := <-r.executeAsync(ctx, tool, params):
		return result.result, result.err
	case <-ctx.Done():
		return nil, fmt.Errorf("tool execution timeout")
	}
}

type toolResult struct {
	result interface{}
	err    error
}

func (r *ToolRegistry) executeAsync(ctx context.Context, tool Tool, params map[string]interface{}) chan toolResult {
	resultCh := make(chan toolResult, 1)

	go func() {
		result, err := tool.Execute(ctx, params)
		resultCh <- toolResult{result: result, err: err}
	}()

	return resultCh
}

// ConvertAllTools 将注册表中所有工具转换为 LLM 可用的工具定义。
// 每个工具的 JSON Schema 参数由各自的 ParametersSchema() 方法返回。
func ConvertAllTools(registry *ToolRegistry) []llm.LLMTool {
	tools := registry.List()
	out := make([]llm.LLMTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, llm.LLMTool{
			Type: "function",
			Function: llm.LLMFunctionDef{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.ParametersSchema(),
			},
		})
	}
	return out
}
