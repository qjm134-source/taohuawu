package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/watertown/guide/internal/knowledge"
)

// GetPlayerInfoTool 获取玩家信息
type GetPlayerInfoTool struct{}

func (t *GetPlayerInfoTool) Name() string {
	return "get_player_info"
}

func (t *GetPlayerInfoTool) Description() string {
	return "获取玩家的基本信息，包括昵称、访问次数等"
}

func (t *GetPlayerInfoTool) Timeout() time.Duration {
	return 5 * time.Second
}

func (t *GetPlayerInfoTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	playerID, ok := params["player_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid player_id")
	}

	// 这里应该从数据库获取，简化实现返回模拟数据
	return map[string]interface{}{
		"player_id":   playerID,
		"nickname":    "玩家",
		"dialogues":   10,
		"first_visit": "2024-01-01",
	}, nil
}

// GetGameGuideTool 获取游戏指南
type GetGameGuideTool struct {
	KB *knowledge.KnowledgeBase
}

func (t *GetGameGuideTool) Name() string {
	return "get_game_guide"
}

func (t *GetGameGuideTool) Description() string {
	return "获取游戏基础指南和操作说明"
}

func (t *GetGameGuideTool) Timeout() time.Duration {
	return 5 * time.Second
}

func (t *GetGameGuideTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	// 从知识库返回基础操作信息
	for _, cat := range t.KB.Categories {
		if cat.Name == "基础操作" {
			return map[string]interface{}{
				"category": cat.Name,
				"questions": cat.Questions,
			}, nil
		}
	}

	return map[string]interface{}{
		"message": "游戏正在开发中，更多内容敬请期待！",
	}, nil
}

// GetQuestInfoTool 获取任务信息
type GetQuestInfoTool struct {
	KB *knowledge.KnowledgeBase
}

func (t *GetQuestInfoTool) Name() string {
	return "get_quest_info"
}

func (t *GetQuestInfoTool) Description() string {
	return "获取任务系统相关信息"
}

func (t *GetQuestInfoTool) Timeout() time.Duration {
	return 5 * time.Second
}

func (t *GetQuestInfoTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	for _, cat := range t.KB.Categories {
		if cat.Name == "任务系统" {
			return map[string]interface{}{
				"category": cat.Name,
				"questions": cat.Questions,
			}, nil
		}
	}

	return map[string]interface{}{
		"message": "任务系统正在完善中...",
	}, nil
}

// GetScenarioInfoTool 获取场景信息
type GetScenarioInfoTool struct {
	KB *knowledge.KnowledgeBase
}

func (t *GetScenarioInfoTool) Name() string {
	return "get_scenario_info"
}

func (t *GetScenarioInfoTool) Description() string {
	return "获取当前场景的描述和信息"
}

func (t *GetScenarioInfoTool) Timeout() time.Duration {
	return 5 * time.Second
}

func (t *GetScenarioInfoTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	desc, err := knowledge.GetScenarioDesc("data/knowledge")
	if err != nil {
		return map[string]interface{}{
			"message": "欢迎来到江南水乡！这里有着独特的水乡风情。",
		}, nil
	}

	return map[string]interface{}{
		"background": desc.Background,
		"npc": map[string]interface{}{
			"name":        desc.NPC.Name,
			"description": desc.NPC.Description,
		},
	}, nil
}
