package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Category 问题分类
type Category struct {
	Name      string     `json:"name"`
	Questions []Question `json:"questions"`
}

// Question 问题
type Question struct {
	Q    string   `json:"q"`
	A    string   `json:"a"`
	Tags []string `json:"tags"`
}

// KnowledgeBase 知识库
type KnowledgeBase struct {
	Categories []Category `json:"categories"`
}

// FAQ 游戏FAQ
type FAQ struct {
	Categories []Category
}

// GameRules 游戏规则
type GameRules struct {
	Rules []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"rules"`
}

// ScenarioDesc 场景描述
type ScenarioDesc struct {
	Background string `json:"background"`
	NPC        struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Position    struct {
			X int `json:"x"`
			Y int `json:"y"`
		} `json:"position"`
	} `json:"npc"`
}

// Load 加载知识库
func Load(path string) (*KnowledgeBase, error) {
	kb := &KnowledgeBase{}

	// 加载 FAQ
	faqFile := filepath.Join(path, "game_faq.json")
	faqData, err := os.ReadFile(faqFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read faq: %w", err)
	}

	var faq FAQ
	if err := json.Unmarshal(faqData, &faq); err != nil {
		return nil, fmt.Errorf("failed to parse faq: %w", err)
	}
	kb.Categories = faq.Categories

	return kb, nil
}

// GetFAQ 加载FAQ
func GetFAQ(path string) (*FAQ, error) {
	file := filepath.Join(path, "game_faq.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read faq: %w", err)
	}

	var faq FAQ
	if err := json.Unmarshal(data, &faq); err != nil {
		return nil, fmt.Errorf("failed to parse faq: %w", err)
	}
	return &faq, nil
}

// GetGameRules 加载游戏规则
func GetGameRules(path string) (*GameRules, error) {
	file := filepath.Join(path, "game_rules.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read game rules: %w", err)
	}

	var rules GameRules
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("failed to parse game rules: %w", err)
	}
	return &rules, nil
}

// GetScenarioDesc 加载场景描述
func GetScenarioDesc(path string) (*ScenarioDesc, error) {
	file := filepath.Join(path, "scenario_desc.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read scenario desc: %w", err)
	}

	var desc ScenarioDesc
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("failed to parse scenario desc: %w", err)
	}
	return &desc, nil
}

// FindQuestion 查找问题
func (kb *KnowledgeBase) FindQuestion(query string) *Question {
	for _, cat := range kb.Categories {
		for _, q := range cat.Questions {
			if q.Q == query {
				return &q
			}
		}
	}
	return nil
}

// FindByTag 根据标签查找问题
func (kb *KnowledgeBase) FindByTag(tag string) []Question {
	var results []Question
	for _, cat := range kb.Categories {
		for _, q := range cat.Questions {
			for _, t := range q.Tags {
				if t == tag {
					results = append(results, q)
					break
				}
			}
		}
	}
	return results
}
