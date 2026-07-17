package tools

import (
	"context"
	"fmt"
	"time"

	eino_tool "github.com/cloudwego/eino/components/tool"
	"github.com/watertown/guide/internal/knowledge"
	"github.com/watertown/guide/internal/weather"
	"github.com/watertown/guide/pkg/logging"
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

func NewToolRegistry(kb *knowledge.KnowledgeBase, weatherService weather.Service, logger logging.Logger) (*ToolRegistry, error) {
	registry := &ToolRegistry{
		tools: make(map[string]eino_tool.InvokableTool),
	}

	playerInfoTool, err := NewGetPlayerInfoTool()
	if err != nil {
		return nil, fmt.Errorf("create player info tool: %w", err)
	}
	registry.Register(playerInfoTool)

	gameGuideTool, err := NewGetGameGuideTool(kb)
	if err != nil {
		return nil, fmt.Errorf("create game guide tool: %w", err)
	}
	registry.Register(gameGuideTool)

	questInfoTool, err := NewGetQuestInfoTool(kb)
	if err != nil {
		return nil, fmt.Errorf("create quest info tool: %w", err)
	}
	registry.Register(questInfoTool)

	scenarioInfoTool, err := NewGetScenarioInfoTool(kb)
	if err != nil {
		return nil, fmt.Errorf("create scenario info tool: %w", err)
	}
	registry.Register(scenarioInfoTool)

	weatherTool, err := NewGetWeatherTool(weatherService, logger)
	if err != nil {
		return nil, fmt.Errorf("create weather tool: %w", err)
	}
	registry.Register(weatherTool)

	return registry, nil
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
