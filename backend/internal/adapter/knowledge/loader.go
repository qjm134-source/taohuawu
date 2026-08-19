package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Category 问题分类。
type Category struct {
	Name      string     `json:"name"`
	Questions []Question `json:"questions"`
}

// Question 知识库中的问答对。
type Question struct {
	Q    string   `json:"q"`
	A    string   `json:"a"`
	Tags []string `json:"tags"`
}

// KnowledgeBase 知识库。
type KnowledgeBase struct {
	Categories []Category `json:"categories"`
}

// FAQ 游戏 FAQ。
type FAQ struct {
	Categories []Category
}

// GameRules 游戏规则。
type GameRules struct {
	Rules []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"rules"`
}

// ScenarioDesc 场景描述。
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

// Load 从 path 目录加载 FAQ 并构建 KnowledgeBase。
func Load(path string) (*KnowledgeBase, error) {
	faq, err := GetFAQ(path)
	if err != nil {
		return nil, fmt.Errorf("load knowledge base: %w", err)
	}

	return &KnowledgeBase{Categories: faq.Categories}, nil
}

// GetFAQ 从 path 目录加载 game_faq.json。
func GetFAQ(path string) (*FAQ, error) {
	file := filepath.Join(path, "game_faq.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read faq file %s: %w", file, err)
	}

	var faq FAQ
	if err := json.Unmarshal(data, &faq); err != nil {
		return nil, fmt.Errorf("parse faq file %s: %w", file, err)
	}
	return &faq, nil
}

// GetGameRules 从 path 目录加载 game_rules.json。
func GetGameRules(path string) (*GameRules, error) {
	file := filepath.Join(path, "game_rules.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read game rules file %s: %w", file, err)
	}

	var rules GameRules
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("parse game rules file %s: %w", file, err)
	}
	return &rules, nil
}

// GetScenarioDesc 从 path 目录加载 scenario_desc.json。
func GetScenarioDesc(path string) (*ScenarioDesc, error) {
	file := filepath.Join(path, "scenario_desc.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read scenario desc file %s: %w", file, err)
	}

	var desc ScenarioDesc
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("parse scenario desc file %s: %w", file, err)
	}
	return &desc, nil
}

// FindQuestion 根据问题文本查找对应的问答对。
func (kb *KnowledgeBase) FindQuestion(query string) *Question {
	for i := range kb.Categories {
		for j := range kb.Categories[i].Questions {
			if kb.Categories[i].Questions[j].Q == query {
				return &kb.Categories[i].Questions[j]
			}
		}
	}
	return nil
}

// FindByTag 根据标签查找所有匹配的问答对。
func (kb *KnowledgeBase) FindByTag(tag string) []Question {
	var results []Question
	for _, cat := range kb.Categories {
		for _, q := range cat.Questions {
			if slices.Contains(q.Tags, tag) {
				results = append(results, q)
			}
		}
	}
	return results
}
