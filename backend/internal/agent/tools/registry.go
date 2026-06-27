package tools

import (
	"context"
	"time"

	eino_tool "github.com/cloudwego/eino/components/tool"
	"github.com/watertown/guide/internal/knowledge"
)

type Tool interface {
	Name() string
	Description() string
	ParametersSchema() map[string]interface{}
	Execute(ctx context.Context, params map[string]interface{}) (interface{}, error)
	Timeout() time.Duration
}

type ToolRegistry struct {
	tools map[string]eino_tool.InvokableTool
}

func NewToolRegistry(kb *knowledge.KnowledgeBase) *ToolRegistry {
	registry := &ToolRegistry{
		tools: make(map[string]eino_tool.InvokableTool),
	}

	registry.Register(NewGetPlayerInfoTool())
	registry.Register(NewGetGameGuideTool(kb))
	registry.Register(NewGetQuestInfoTool(kb))
	registry.Register(NewGetScenarioInfoTool(kb))
	registry.Register(NewGetWeatherTool())

	return registry
}

func (r *ToolRegistry) Register(tool eino_tool.InvokableTool) {
	info, _ := tool.Info(context.Background())
	r.tools[info.Name] = tool
}

func (r *ToolRegistry) Get(name string) (eino_tool.InvokableTool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) List() []eino_tool.InvokableTool {
	result := make([]eino_tool.InvokableTool, 0, len(r.tools))
	for _, tool := range r.tools {
		result = append(result, tool)
	}
	return result
}
