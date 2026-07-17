package tools

import (
	"context"
	"fmt"

	eino_tool "github.com/cloudwego/eino/components/tool"
	eino_tool_utils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/watertown/guide/internal/knowledge"
)

type GetPlayerInfoInput struct {
	PlayerID string `json:"player_id" jsonschema:"required" jsonschema_description:"需要查询的玩家 ID"`
}

type GetPlayerInfoOutput struct {
	PlayerID   string `json:"player_id"`
	Nickname   string `json:"nickname"`
	Dialogues  int    `json:"dialogues"`
	FirstVisit string `json:"first_visit"`
}

func NewGetPlayerInfoTool() (eino_tool.InvokableTool, error) {
	return eino_tool_utils.InferTool[GetPlayerInfoInput, GetPlayerInfoOutput](
		"get_player_info",
		"获取玩家的基本信息，包括昵称、访问次数等",
		func(ctx context.Context, input GetPlayerInfoInput) (GetPlayerInfoOutput, error) {
			if input.PlayerID == "" {
				return GetPlayerInfoOutput{}, fmt.Errorf("invalid player_id")
			}
			return GetPlayerInfoOutput{
				PlayerID:   input.PlayerID,
				Nickname:   "玩家",
				Dialogues:  10,
				FirstVisit: "2024-01-01",
			}, nil
		},
	)
}

type GetGameGuideInput struct{}

type GetGameGuideOutput struct {
	Category  string               `json:"category"`
	Questions []knowledge.Question `json:"questions"`
}

type getGameGuideToolImpl struct {
	KB *knowledge.KnowledgeBase
}

func (t *getGameGuideToolImpl) invoke(ctx context.Context, input GetGameGuideInput) (GetGameGuideOutput, error) {
	for _, cat := range t.KB.Categories {
		if cat.Name == "基础操作" {
			return GetGameGuideOutput{
				Category:  cat.Name,
				Questions: cat.Questions,
			}, nil
		}
	}
	return GetGameGuideOutput{
		Category:  "游戏指南",
		Questions: nil,
	}, nil
}

func NewGetGameGuideTool(kb *knowledge.KnowledgeBase) (eino_tool.InvokableTool, error) {
	impl := &getGameGuideToolImpl{KB: kb}
	return eino_tool_utils.InferTool[GetGameGuideInput, GetGameGuideOutput](
		"get_game_guide",
		"获取游戏基础指南和操作说明",
		impl.invoke,
	)
}

type GetQuestInfoInput struct{}

type GetQuestInfoOutput struct {
	Category  string               `json:"category"`
	Questions []knowledge.Question `json:"questions"`
}

type getQuestInfoToolImpl struct {
	KB *knowledge.KnowledgeBase
}

func (t *getQuestInfoToolImpl) invoke(ctx context.Context, input GetQuestInfoInput) (GetQuestInfoOutput, error) {
	for _, cat := range t.KB.Categories {
		if cat.Name == "任务系统" {
			return GetQuestInfoOutput{
				Category:  cat.Name,
				Questions: cat.Questions,
			}, nil
		}
	}
	return GetQuestInfoOutput{
		Category:  "任务系统",
		Questions: nil,
	}, nil
}

func NewGetQuestInfoTool(kb *knowledge.KnowledgeBase) (eino_tool.InvokableTool, error) {
	impl := &getQuestInfoToolImpl{KB: kb}
	return eino_tool_utils.InferTool[GetQuestInfoInput, GetQuestInfoOutput](
		"get_quest_info",
		"获取任务系统相关信息",
		impl.invoke,
	)
}

type GetScenarioInfoInput struct{}

type GetScenarioInfoOutput struct {
	Background string `json:"background"`
	NPC        struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"npc"`
}

type getScenarioInfoToolImpl struct {
	KB *knowledge.KnowledgeBase
}

func (t *getScenarioInfoToolImpl) invoke(ctx context.Context, input GetScenarioInfoInput) (GetScenarioInfoOutput, error) {
	desc, err := knowledge.GetScenarioDesc("data/knowledge")
	if err != nil {
		return GetScenarioInfoOutput{
			Background: "欢迎来到江南水乡！这里有着独特的水乡风情。",
		}, nil
	}
	return GetScenarioInfoOutput{
		Background: desc.Background,
		NPC: struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}{
			Name:        desc.NPC.Name,
			Description: desc.NPC.Description,
		},
	}, nil
}

func NewGetScenarioInfoTool(kb *knowledge.KnowledgeBase) (eino_tool.InvokableTool, error) {
	impl := &getScenarioInfoToolImpl{KB: kb}
	return eino_tool_utils.InferTool[GetScenarioInfoInput, GetScenarioInfoOutput](
		"get_scenario_info",
		"获取当前场景的描述和信息",
		impl.invoke,
	)
}
