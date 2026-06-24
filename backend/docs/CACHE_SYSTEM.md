# 缓存系统设计

本项目实现了多层缓存系统，用于优化 LLM 调用成本和响应延迟。缓存系统包括精确匹配缓存、语义缓存、工具结果缓存等多种类型。

---

## 1. 架构概览

```
┌─────────────────────────────────────────────────────────────┐
│                    客户端请求                               │
└────────────────────────────────┬────────────────────────────┘
                                 │
                                 ▼
┌─────────────────────────────────────────────────────────────┐
│  第一层：精确匹配缓存 (Exact Match)                        │
│  缓存键：SHA256(question:model)                            │
│  命中率：60-80%（FAQ 场景）                                 │
│  响应时间：<0.01ms                                          │
└────────────────────────────────┬────────────────────────────┘
                                 │ 未命中
                                 ▼
┌─────────────────────────────────────────────────────────────┐
│  第二层：语义缓存 (Semantic Cache)                         │
│  使用 Embedding 向量相似度匹配                             │
│  相似度阈值：0.85                                          │
│  命中率：额外 10-20%                                        │
│  响应时间：50-200ms（Embedding API）                       │
└────────────────────────────────┬────────────────────────────┘
                                 │ 未命中
                                 ▼
┌─────────────────────────────────────────────────────────────┐
│  第三层：工具结果缓存 (Tool Result Cache)                  │
│  缓存键：SHA256(tool_name:params)                          │
│  命中率：70-90%（相同参数的工具调用）                       │
│  响应时间：<0.01ms                                          │
└────────────────────────────────┬────────────────────────────┘
                                 │ 未命中
                                 ▼
                          LLM API 调用
                          响应时间：500ms - 10s
```

---

## 2. 缓存类型详解

### 2.1 精确匹配缓存

**原理**：使用 SHA256 哈希生成缓存键，完全匹配问题和模型。

```go
key := hash(question + ":" + model)
```

**特点**：
- 响应时间极快（<0.01ms）
- 命中率高（FAQ 场景 60-80%）
- 无额外 API 调用成本

**适用场景**：
- 重复提问相同问题
- FAQ 类应用
- 批量处理相同请求

### 2.2 语义缓存

**原理**：使用 Embedding 向量计算相似度，匹配语义相似的问题。

```go
embedding := embeddingAPI.GetEmbedding(question)
similarity := embeddingAPI.Similarity(embedding, cachedEmbedding)
if similarity > threshold {
    return cachedAnswer
}
```

**特点**：
- 响应时间中等（50-200ms，取决于 Embedding API）
- 命中率额外提升 10-20%
- 需要调用 Embedding API

**适用场景**：
- 用户用不同表达方式问相同问题
- 相似意图的问题
- 多语言相似问题

**示例**：

| 用户提问 | 缓存问题 | 相似度 | 命中？ |
|---------|---------|--------|--------|
| "今天天气怎么样？" | "今天天气如何？" | 0.92 | ✅ 命中 |
| "明天会下雨吗？" | "今天天气如何？" | 0.78 | ❌ 未命中 |
| "请问游戏规则是什么" | "游戏规则是什么？" | 0.98 | ✅ 命中 |

### 2.3 工具结果缓存

**原理**：缓存外部工具调用的结果，避免重复调用。

```go
key := hash(tool_name + ":" + params)
```

**特点**：
- 响应时间极快（<0.01ms）
- 命中率高（相同参数的工具调用 70-90%）
- 减少外部 API 调用

**适用场景**：
- 天气查询（相同城市）
- 地理位置查询
- 任何参数化的外部 API 调用

---

## 3. 配置说明

### 3.1 基础配置

```yaml
cost:
  cache_enabled: true              # 是否启用缓存
  similarity_threshold: 0.85       # 语义相似度阈值（0.8-0.9）
```

### 3.2 Embedding 配置

```yaml
cost:
  embedding:
    enabled: true                  # 是否启用语义缓存
    type: remote                   # remote（OpenAI API）| local（本地模型）
    api_key: ${OPENAI_API_KEY}     # API Key（仅 remote 需要）
    base_url: ""                   # API 地址（可选）
    model: text-embedding-3-small  # Embedding 模型
```

### 3.3 配置项说明

| 配置项 | 说明 | 默认值 |
|-------|------|--------|
| `cache_enabled` | 是否启用缓存 | `true` |
| `similarity_threshold` | 语义相似度阈值 | `0.85` |
| `embedding.enabled` | 是否启用语义缓存 | `true` |
| `embedding.type` | Embedding 类型 | `remote` |
| `embedding.model` | Embedding 模型 | `text-embedding-3-small` |

---

## 4. Embedding 模型选择

### 4.1 远程 API（推荐生产）

| 模型 | 向量维度 | 成本 | 适用场景 |
|------|---------|------|---------|
| `text-embedding-3-small` | 1536 | $0.00002/1K tokens | 通用场景（推荐） |
| `text-embedding-3-large` | 3072 | $0.00013/1K tokens | 高精度场景 |

**优势**：
- 模型质量高
- 中文语义理解好
- 无需本地部署

**劣势**：
- 需要 API Key
- 有调用成本

### 4.2 本地模型（开发测试）

| 模型 | 向量维度 | 模型大小 | 适用场景 |
|------|---------|---------|---------|
| `all-MiniLM-L6-v2` | 384 | ~80MB | 开发测试 |
| `all-MiniLM-L12-v2` | 384 | ~150MB | 更高精度 |
| `all-mpnet-base-v2` | 768 | ~450MB | 最高精度 |

**优势**：
- 无需 API Key
- 无调用成本
- 响应快

**劣势**：
- 需要本地部署
- 中文语义理解较弱
- 需要维护模型

---

## 5. 性能分析

### 5.1 缓存查询开销

| 操作 | 时间开销 | API 调用 |
|------|---------|---------|
| 精确匹配缓存 | ~0.01ms | 无 |
| 语义缓存（Embedding） | ~50-200ms | 1 次 |
| 工具结果缓存 | ~0.01ms | 无 |
| LLM API 调用 | ~500ms - 10s | 1 次 |

### 5.2 命中率分析

| 场景 | 精确匹配命中率 | 语义缓存命中率 | 总命中率 |
|------|---------------|---------------|---------|
| FAQ 类应用 | 80-95% | 5-10% | 85-100% |
| 聊天机器人 | 30-60% | 10-20% | 40-80% |
| 个性化助手 | 10-30% | 5-15% | 15-45% |

### 5.3 成本节省

假设场景：1000 次请求，命中率 70%

| 方案 | LLM 调用次数 | 成本（假设 $0.01/次） |
|------|-------------|---------------------|
| 无缓存 | 1000 | $10 |
| 精确匹配缓存 | 300 | $3 |
| 语义缓存（额外） | 250 | $2.5 |

**节省成本**：$7.5（75%）

---

## 6. 最佳实践

### 6.1 相似度阈值选择

| 阈值 | 命中率 | 准确率 | 适用场景 |
|------|-------|-------|---------|
| 0.75 | 高 | 低 | 宽松匹配，可能误匹配 |
| 0.85 | 中 | 高 | **推荐默认值** |
| 0.95 | 低 | 极高 | 严格匹配，几乎无误匹配 |

### 6.2 缓存容量管理

```yaml
cost:
  max_entries: 10000    # 最大缓存条目数
  ttl: 1h               # 缓存有效期
```

**建议**：
- 根据内存大小设置 `max_entries`
- 根据数据实时性设置 `ttl`
- 天气等实时数据建议短 TTL（1h）
- FAQ 等静态数据建议长 TTL（24h）

### 6.3 异步索引构建

语义缓存的 Embedding 索引是异步构建的，不阻塞主流程：

```go
// 设置缓存时，异步构建语义索引
if c.embeddingAPI != nil {
    go c.buildSemanticIndex(ctx, question, answer, model, tokensSaved)
}
```

### 6.4 缓存监控

通过 Prometheus 监控缓存命中率：

```promql
# 缓存命中率
sum(cache_hits_total) / (sum(cache_hits_total) + sum(cache_misses_total))

# 各类型缓存命中分布
sum by (cache_type) (cache_hits_total)
```

---

## 7. 实现细节

### 7.1 缓存键生成

```go
// 精确匹配缓存键
func hashWithModel(question, model string) string {
    return hash(question + ":" + model)
}

// 工具结果缓存键
func hashToolResult(toolName, params string) string {
    return hash(toolName + ":" + params)
}

// SHA256 哈希
func hash(s string) string {
    h := sha256.New()
    h.Write([]byte(s))
    return fmt.Sprintf("%x", h.Sum(nil))
}
```

### 7.2 语义相似度计算

```go
// 余弦相似度
func Similarity(a, b []float32) float64 {
    var dotProduct, normA, normB float64
    for i := range a {
        dotProduct += float64(a[i]) * float64(b[i])
        normA += float64(a[i]) * float64(a[i])
        normB += float64(b[i]) * float64(b[i])
    }
    return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}
```

### 7.3 缓存条目结构

```go
type CacheEntry struct {
    Question     string
    Answer       string
    CreatedAt    time.Time
    Model        string
    TokensSaved  int
    Type         CacheType
    Embedding    []float32  // 语义缓存的 embedding 向量
}
```

---

## 8. 关键代码位置

| 文件 | 说明 |
|------|------|
| `internal/cost/cache.go` | 缓存接口定义 |
| `internal/cost/layered_cache.go` | 多层缓存实现 |
| `internal/cost/embedding.go` | Embedding API 客户端 |
| `internal/cost/optimizer.go` | 缓存管理器 |
| `internal/observability/metrics.go` | 缓存指标定义 |

---

## 9. 相关文档

- [可观测性指南](OBSERVABILITY.md) — 缓存监控指标
- [多模型路由系统](MULTI_MODEL_ROUTER.md) — 成本优化策略
- [后端 README](../README.md)