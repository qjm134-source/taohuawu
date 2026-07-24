package emotion

import "testing"

func TestRuleBasedDetector_Detect(t *testing.T) {
	detector := NewRuleBasedDetector()

	tests := []struct {
		name    string
		message string
		want    Emotion
	}{
		{
			name:    "happy keywords",
			message: "今天天气真好，我好开心啊",
			want:    EmotionHappy,
		},
		{
			name:    "sad keywords",
			message: "我很难过，不想玩了",
			want:    EmotionSad,
		},
		{
			name:    "angry keywords",
			message: "这游戏太垃圾了，真讨厌",
			want:    EmotionAngry,
		},
		{
			name:    "confused keywords",
			message: "这个任务怎么完成？我不懂",
			want:    EmotionConfused,
		},
		{
			name:    "excited keywords",
			message: "哇，太棒了，超级期待",
			want:    EmotionExcited,
		},
		{
			name:    "neutral by default",
			message: "你好，请问现在几点",
			want:    EmotionNeutral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detector.Detect(tt.message)
			if got != tt.want {
				t.Errorf("Detect(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestGetEmoji(t *testing.T) {
	tests := []struct {
		emotion Emotion
		want    string
	}{
		{EmotionHappy, "😊"},
		{EmotionSad, "😢"},
		{EmotionAngry, "😠"},
		{EmotionConfused, "😕"},
		{EmotionExcited, "🎉"},
		{EmotionNeutral, "😊"},
	}

	for _, tt := range tests {
		t.Run(string(tt.emotion), func(t *testing.T) {
			got := GetEmoji(tt.emotion)
			if got != tt.want {
				t.Errorf("GetEmoji(%v) = %v, want %v", tt.emotion, got, tt.want)
			}
		})
	}
}
