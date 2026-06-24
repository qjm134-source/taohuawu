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

// LocalEmbeddingClient 本地 Embedding 客户端。
// 支持多种后端：Ollama、HuggingFace TEI（Text Embeddings Inference）、
// 或任何兼容 OpenAI Embedding API 的本地服务。
// 若所有后端不可用，自动降级为伪向量（仅用于开发调试）。
type LocalEmbeddingClient struct {
	modelName  string
	dimensions int
	baseURL    string   // 本地服务地址，如 http://localhost:11434 (Ollama)
	serverType string   // "ollama" | "tei" | "openai-compat"
	enabled    bool     // 是否已配置真实后端
	client     *http.Client
}

// LocalEmbeddingConfig 本地 Embedding 配置
type LocalEmbeddingConfig struct {
	ModelName  string // 模型名称，如 "nomic-embed-text" (Ollama) 或 "BAAI/bge-small-en-v1.5" (TEI)
	BaseURL    string // 本地服务地址，如 http://localhost:11434
	ServerType string // 后端类型: "ollama" | "tei" | "openai-compat"
}

// NewLocalEmbeddingClient 创建本地 Embedding 客户端
func NewLocalEmbeddingClient(modelName string) *LocalEmbeddingClient {
	return NewLocalEmbeddingClientWithConfig(LocalEmbeddingConfig{
		ModelName:  modelName,
		BaseURL:    "http://localhost:11434",
		ServerType: "ollama",
	})
}

// NewLocalEmbeddingClientWithConfig 使用完整配置创建客户端
func NewLocalEmbeddingClientWithConfig(cfg LocalEmbeddingConfig) *LocalEmbeddingClient {
	if cfg.ModelName == "" {
		cfg.ModelName = "nomic-embed-text"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	if cfg.ServerType == "" {
		cfg.ServerType = "ollama"
	}

	dimensions := 384 // default fallback
	switch {
	case strings.Contains(cfg.ModelName, "mpnet") || strings.Contains(cfg.ModelName, "distilroberta"):
		dimensions = 768
	case strings.Contains(cfg.ModelName, "large"):
		dimensions = 1024
	case strings.Contains(cfg.ModelName, "bge-m3"):
		dimensions = 1024
	}

	return &LocalEmbeddingClient{
		modelName:  cfg.ModelName,
		dimensions: dimensions,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		serverType: cfg.ServerType,
		enabled:    cfg.ModelName != "",
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

// GetEmbedding 获取文本的 Embedding 向量。
// 优先调用真实模型服务，失败时降级为伪向量（开发模式可用）。
func (c *LocalEmbeddingClient) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	if c.enabled {
		emb, err := c.callRealEmbedding(ctx, text)
		if err == nil {
			return emb, nil
		}
		// 真实服务不可用时降级，避免阻塞
	}
	return c.generatePseudoEmbedding(text), nil
}

// callRealEmbedding 调用真实 Embedding 服务。
func (c *LocalEmbeddingClient) callRealEmbedding(ctx context.Context, text string) ([]float32, error) {
	switch c.serverType {
	case "ollama":
		return c.callOllamaEmbedding(ctx, text)
	case "tei":
		return c.callTEIEmbedding(ctx, text)
	case "openai-compat":
		return c.callOpenAICompatEmbedding(ctx, text)
	default:
		return nil, fmt.Errorf("unknown server type: %s", c.serverType)
	}
}

// callOllamaEmbedding 调用 Ollama Embedding API。
// API 文档: https://github.com/ollama/ollama/blob/main/docs/api.md#generate-embeddings
func (c *LocalEmbeddingClient) callOllamaEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := map[string]interface{}{
		"model":  c.modelName,
		"prompt": text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embedding returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse ollama embedding response: %w", err)
	}

	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama returned empty embedding")
	}

	// 更新实际维度
	c.dimensions = len(result.Embedding)
	return result.Embedding, nil
}

// callTEIEmbedding 调用 HuggingFace Text Embeddings Inference API。
// API 文档: https://huggingface.github.io/text-embeddings-inference/
func (c *LocalEmbeddingClient) callTEIEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := map[string]interface{}{
		"inputs": text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/embed", strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tei embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tei embedding returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// TEI 返回格式: [[0.1, 0.2, ...]]
	var result [][]float32
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tei embedding response: %w", err)
	}

	if len(result) == 0 || len(result[0]) == 0 {
		return nil, fmt.Errorf("tei returned empty embedding")
	}

	c.dimensions = len(result[0])
	return result[0], nil
}

// callOpenAICompatEmbedding 调用兼容 OpenAI API 格式的本地 Embedding 服务。
func (c *LocalEmbeddingClient) callOpenAICompatEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := map[string]interface{}{
		"input": text,
		"model": c.modelName,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/embeddings", strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai-compat embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai-compat embedding returned status %d: %s", resp.StatusCode, string(body))
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
		return nil, fmt.Errorf("failed to parse openai-compat embedding response: %w", err)
	}

	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai-compat returned empty embedding")
	}

	c.dimensions = len(result.Data[0].Embedding)
	return result.Data[0].Embedding, nil
}

// generatePseudoEmbedding 生成伪向量（降级开发模式）。
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
