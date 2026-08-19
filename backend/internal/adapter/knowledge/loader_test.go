package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeTempJSON 在临时目录创建一个 JSON 文件并返回路径。
func writeTempJSON(t *testing.T, dir, filename string, v interface{}) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON for %s: %v", filename, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}

func TestGetFAQ(t *testing.T) {
	dir := t.TempDir()

	faqData := FAQ{
		Categories: []Category{
			{
				Name: "基础操作",
				Questions: []Question{
					{Q: "怎么移动？", A: "使用 WASD 或方向键移动", Tags: []string{"操作", "新手"}},
					{Q: "怎么接任务？", A: "点击有感叹号的 NPC", Tags: []string{"任务"}},
				},
			},
			{
				Name: "经济系统",
				Questions: []Question{
					{Q: "怎么赚钱？", A: "完成任务、参与活动", Tags: []string{"金币"}},
				},
			},
		},
	}
	writeTempJSON(t, dir, "game_faq.json", faqData)

	got, err := GetFAQ(dir)
	if err != nil {
		t.Fatalf("GetFAQ() error = %v, want nil", err)
	}
	if len(got.Categories) != 2 {
		t.Fatalf("len(Categories) = %d, want 2", len(got.Categories))
	}
	if got.Categories[0].Name != "基础操作" {
		t.Errorf("Categories[0].Name = %q, want %q", got.Categories[0].Name, "基础操作")
	}
	if len(got.Categories[0].Questions) != 2 {
		t.Errorf("len(Questions) = %d, want 2", len(got.Categories[0].Questions))
	}
	if got.Categories[0].Questions[0].A != "使用 WASD 或方向键移动" {
		t.Errorf("Questions[0].A = %q, want %q", got.Categories[0].Questions[0].A, "使用 WASD 或方向键移动")
	}
}

func TestGetFAQ_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := GetFAQ(dir)
	if err == nil {
		t.Fatal("GetFAQ() error = nil, want non-nil (file not found)")
	}
}

func TestGetGameRules(t *testing.T) {
	dir := t.TempDir()

	rulesData := GameRules{
		Rules: []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}{
			{Name: "禁止作弊", Description: "不要使用外挂"},
			{Name: "尊重他人", Description: "文明交流"},
		},
	}
	writeTempJSON(t, dir, "game_rules.json", rulesData)

	got, err := GetGameRules(dir)
	if err != nil {
		t.Fatalf("GetGameRules() error = %v, want nil", err)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("len(Rules) = %d, want 2", len(got.Rules))
	}
	if got.Rules[0].Name != "禁止作弊" {
		t.Errorf("Rules[0].Name = %q, want %q", got.Rules[0].Name, "禁止作弊")
	}
}

func TestGetScenarioDesc(t *testing.T) {
	dir := t.TempDir()

	descData := ScenarioDesc{
		Background: "江南水乡，风景如画",
	}
	descData.NPC.Name = "小荷"
	descData.NPC.Description = "导游"
	descData.NPC.Position.X = 100
	descData.NPC.Position.Y = 200
	writeTempJSON(t, dir, "scenario_desc.json", descData)

	got, err := GetScenarioDesc(dir)
	if err != nil {
		t.Fatalf("GetScenarioDesc() error = %v, want nil", err)
	}
	if got.Background != "江南水乡，风景如画" {
		t.Errorf("Background = %q, want %q", got.Background, "江南水乡，风景如画")
	}
	if got.NPC.Name != "小荷" {
		t.Errorf("NPC.Name = %q, want %q", got.NPC.Name, "小荷")
	}
	if got.NPC.Position.X != 100 || got.NPC.Position.Y != 200 {
		t.Errorf("NPC.Position = (%d,%d), want (100,200)", got.NPC.Position.X, got.NPC.Position.Y)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	faqData := FAQ{
		Categories: []Category{
			{
				Name: "基础操作",
				Questions: []Question{
					{Q: "怎么移动？", A: "WASD", Tags: []string{"操作"}},
				},
			},
		},
	}
	writeTempJSON(t, dir, "game_faq.json", faqData)

	kb, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if len(kb.Categories) != 1 {
		t.Fatalf("len(Categories) = %d, want 1", len(kb.Categories))
	}
}

func TestKnowledgeBase_FindQuestion(t *testing.T) {
	kb := &KnowledgeBase{
		Categories: []Category{
			{
				Name: "cat1",
				Questions: []Question{
					{Q: "q1", A: "a1"},
					{Q: "q2", A: "a2"},
				},
			},
			{
				Name: "cat2",
				Questions: []Question{
					{Q: "q3", A: "a3"},
				},
			},
		},
	}

	tests := []struct {
		name  string
		query string
		wantA string
		found bool
	}{
		{"found q1", "q1", "a1", true},
		{"found q3", "q3", "a3", true},
		{"not found", "q99", "", false},
		{"empty query", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kb.FindQuestion(tt.query)
			if (got != nil) != tt.found {
				t.Fatalf("FindQuestion(%q) found = %v, want %v", tt.query, got != nil, tt.found)
			}
			if tt.found && got.A != tt.wantA {
				t.Errorf("FindQuestion(%q).A = %q, want %q", tt.query, got.A, tt.wantA)
			}
		})
	}
}

func TestKnowledgeBase_FindByTag(t *testing.T) {
	kb := &KnowledgeBase{
		Categories: []Category{
			{
				Name: "cat1",
				Questions: []Question{
					{Q: "q1", A: "a1", Tags: []string{"tag1", "tag2"}},
					{Q: "q2", A: "a2", Tags: []string{"tag2"}},
				},
			},
			{
				Name: "cat2",
				Questions: []Question{
					{Q: "q3", A: "a3", Tags: []string{"tag1"}},
					{Q: "q4", A: "a4"}, // 无 tag
				},
			},
		},
	}

	tests := []struct {
		name string
		tag  string
		want int
	}{
		{"tag1 matches 2", "tag1", 2},
		{"tag2 matches 2", "tag2", 2},
		{"nonexistent tag", "tag99", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kb.FindByTag(tt.tag)
			if len(got) != tt.want {
				t.Errorf("FindByTag(%q) len = %d, want %d", tt.tag, len(got), tt.want)
			}
		})
	}
}
