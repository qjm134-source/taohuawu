package cost

import "testing"

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a    []float32
		b    []float32
		want float64
	}{
		{
			name: "same vector equals 1",
			a:    []float32{1, 0, 0},
			b:    []float32{1, 0, 0},
			want: 1.0,
		},
		{
			name: "orthogonal vectors equals 0",
			a:    []float32{1, 0, 0},
			b:    []float32{0, 1, 0},
			want: 0.0,
		},
		{
			name: "zero vectors equals 0",
			a:    []float32{0, 0, 0},
			b:    []float32{1, 1, 1},
			want: 0.0,
		},
		{
			name: "different lengths equals 0",
			a:    []float32{1, 0},
			b:    []float32{1, 0, 0},
			want: 0.0,
		},
		{
			name: "opposite vectors equals -1",
			a:    []float32{1, 0, 0},
			b:    []float32{-1, 0, 0},
			want: -1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			const epsilon = 1e-9
			if got < tt.want-epsilon || got > tt.want+epsilon {
				t.Errorf("cosineSimilarity(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestLocalEmbeddingClient_GeneratePseudoEmbedding(t *testing.T) {
	client := NewLocalEmbeddingClientWithConfig(LocalEmbeddingConfig{
		ModelName:  "test",
		BaseURL:    "http://localhost:11434",
		ServerType: "ollama",
	})

	embedding := client.generatePseudoEmbedding("hello world")
	if len(embedding) != client.dimensions {
		t.Errorf("len(embedding) = %d, want %d", len(embedding), client.dimensions)
	}

	// 相同文本应该生成相同的伪向量
	embedding2 := client.generatePseudoEmbedding("hello world")
	for i := range embedding {
		if embedding[i] != embedding2[i] {
			t.Errorf("embedding[%d] = %v, want %v", i, embedding[i], embedding2[i])
		}
	}
}
