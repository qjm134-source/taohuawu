package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// EmbeddingAPI Embedding 接口
type EmbeddingAPI interface {
	GetEmbedding(ctx context.Context, text string) ([]float32, error)
	Similarity(a, b []float32) float64
}

// OpenAIEmbeddingClient OpenAI Embedding API 客户端（需要 API Key）
type OpenAIEmbeddingClient struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewOpenAIEmbeddingClient 创建 OpenAI Embedding 客户端
func NewOpenAIEmbeddingClient(apiKey, baseURL, model string) *OpenAIEmbeddingClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	return &OpenAIEmbeddingClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetEmbedding 获取文本的 Embedding 向量
func (c *OpenAIEmbeddingClient) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}

	maxTokens := 8191
	if len(text) > maxTokens {
		text = text[:maxTokens]
	}

	reqBody := map[string]interface{}{
		"input": text,
		"model": c.model,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(c.baseURL + "/embeddings")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return result.Data[0].Embedding, nil
}

// Similarity 计算两个向量的余弦相似度
func (c *OpenAIEmbeddingClient) Similarity(a, b []float32) float64 {
	return cosineSimilarity(a, b)
}

// LocalEmbeddingClient 本地 Embedding 客户端（无需 API Key）
// 使用本地模型，如 all-MiniLM-L6-v2, sentence-transformers 等
type LocalEmbeddingClient struct {
	modelName  string
	dimensions int
}

// NewLocalEmbeddingClient 创建本地 Embedding 客户端
// 支持的模型: all-MiniLM-L6-v2, all-MiniLM-L12-v2, all-distilroberta-v1
func NewLocalEmbeddingClient(modelName string) *LocalEmbeddingClient {
	dimensions := 384 // default for all-MiniLM-L6-v2
	switch modelName {
	case "all-MiniLM-L12-v2":
		dimensions = 384
	case "all-distilroberta-v1":
		dimensions = 768
	case "all-mpnet-base-v2":
		dimensions = 768
	}

	return &LocalEmbeddingClient{
		modelName:  modelName,
		dimensions: dimensions,
	}
}

// GetEmbedding 获取文本的 Embedding 向量（本地实现）
// 注意：这是一个简化实现，实际生产环境中需要集成 Go 机器学习库
func (c *LocalEmbeddingClient) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	// 实际项目中，这里会调用本地模型
	// 例如使用 go-transformers 或其他 ML 库

	// 当前实现使用简单的哈希生成伪向量（仅用于演示）
	// 生产环境请替换为真实的本地模型调用
	return c.generatePseudoEmbedding(text), nil
}

// generatePseudoEmbedding 生成伪向量（用于演示）
func (c *LocalEmbeddingClient) generatePseudoEmbedding(text string) []float32 {
	embedding := make([]float32, c.dimensions)
	hashVal := hash(text)

	for i := 0; i < c.dimensions; i++ {
		byteIdx := i % len(hashVal)
		embedding[i] = float32(hashVal[byteIdx]) / 255.0
	}

	return embedding
}

// Similarity 计算两个向量的余弦相似度
func (c *LocalEmbeddingClient) Similarity(a, b []float32) float64 {
	return cosineSimilarity(a, b)
}

// cosineSimilarity 计算余弦相似度
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0.0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (sqrt(normA) * sqrt(normB))
}

// sqrt 计算平方根
func sqrt(x float64) float64 {
	if x == 0 {
		return 0
	}
	z := x
	for i := 0; i < 10; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}
