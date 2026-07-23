package emotion

import (
	"strings"
)

// Emotion 情绪类型
type Emotion string

const (
	EmotionNeutral  Emotion = "neutral"
	EmotionHappy    Emotion = "happy"
	EmotionSad      Emotion = "sad"
	EmotionAngry    Emotion = "angry"
	EmotionConfused Emotion = "confused"
	EmotionExcited  Emotion = "excited"
)

// Detector 情绪检测器
type Detector interface {
	Detect(message string) Emotion
}

// RuleBasedDetector 基于规则的情绪检测器
type RuleBasedDetector struct {
	rules map[Emotion][]string
}

// NewRuleBasedDetector 创建基于规则的检测器
func NewRuleBasedDetector() *RuleBasedDetector {
	return &RuleBasedDetector{
		rules: map[Emotion][]string{
			EmotionHappy: {
				"开心", "快乐", "高兴", "喜欢", "爱", "棒", "好", "太好了", "哈哈", "谢谢", "感谢",
				"好玩", "有趣", "棒极了", "赞", "漂亮", "美丽", "可爱",
			},
			EmotionSad: {
				"难过", "伤心", "难过", "悲伤", "哭", "遗憾", "可惜", "可惜了",
			},
			EmotionAngry: {
				"生气", "愤怒", "讨厌", "烦", "烦人", "讨厌", "无语", "垃圾", "烂", "怎么这样", "真讨厌",
			},
			EmotionConfused: {
				"不懂", "不明白", "不清楚", "怎么", "如何", "为什么", "什么", "怎么弄", "怎么玩",
			},
			EmotionExcited: {
				"哇", "太棒了", "太好了", "超级", "超", "太", "激动", "兴奋", "期待",
			},
		},
	}
}

// Detect 检测情绪。
// 规则关键词均为中文或小写英文，直接对原始消息做子串匹配即可，避免不必要的转换。
func (d *RuleBasedDetector) Detect(message string) Emotion {
	scores := make(map[Emotion]int)
	for emotion, keywords := range d.rules {
		for _, kw := range keywords {
			if strings.Contains(message, kw) {
				scores[emotion]++
			}
		}
	}

	// 找出得分最高的情绪
	maxScore := 0
	result := EmotionNeutral
	for emotion, score := range scores {
		if score > maxScore {
			maxScore = score
			result = emotion
		}
	}

	return result
}

// GetEmoji 根据情绪获取表情符号
func GetEmoji(emotion Emotion) string {
	switch emotion {
	case EmotionHappy:
		return "😊"
	case EmotionSad:
		return "😢"
	case EmotionAngry:
		return "😠"
	case EmotionConfused:
		return "😕"
	case EmotionExcited:
		return "🎉"
	default:
		return "😊"
	}
}
