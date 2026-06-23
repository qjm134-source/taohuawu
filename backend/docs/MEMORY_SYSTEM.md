# Memory 系统设计文档

本文档详细说明江南水乡智能导游系统的 Memory（记忆）管理方案，包括业界常见方案对比、本项目实现方式及核心代码解析。

---

## 目录

- [1. 问题背景](#1-问题背景)
- [2. 业界常见 Memory 方案](#2-业界常见-memory-方案)
- [3. 本项目实现方案](#3-本项目实现方案)
- [4. 核心代码解析](#4-核心代码解析)
- [5. 配置说明](#5-配置说明)
- [6. 扩展建议](#6-扩展建议)

---

## 1. 问题背景

在 AI 对话系统中，如何在有限的上下文窗口（Context Window）内保持对话连贯性，同时控制 Token 消耗成本，是一个核心挑战。

### 核心矛盾

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│   对话越长 ──────────────────────────────► Token 越多      │
│                                                             │
│   Token 越多 ──────────────────────────────► 成本越高       │
│                                                             │
│   Token 越多 ──────────────────────────────► 可能超限       │
│                                                             │
│   但对话上下文对理解用户意图至关重要！                       │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### 解决方案

通过 Memory 管理，在保持对话上下文的同时，自动压缩和摘要历史消息，实现成本与效果的平衡。

---

## 2. 业界常见 Memory 方案

### 2.1 滑动窗口法（Sliding Window）

**原理**：只保留最近 N 条消息，丢弃更早的历史。

```
原始消息: [M1][M2][M3][M4][M5][M6][M7][M8][M9][M10]
                    │
                    ▼
保留结果:           [M7][M8][M9][M10]
```

**优点**：
- 实现简单，无额外计算开销
- 延迟低，适合实时对话

**缺点**：
- 可能丢失重要上下文
- 无法处理超长对话的关键信息

**适用场景**：简单聊天机器人、场景单一的对话系统

---

### 2.2 规则关键词提取

**原理**：基于规则（关键词、实体识别）筛选关键消息保留。

```go
func extractKeyMessages(messages []Message) []Message {
    keyMessages := []Message{}
    keywords := []string{"任务", "重要", "注意", "需要", "记得", "之前说的"}
    
    for _, msg := range messages {
        if containsAnyKeyword(msg.Content, keywords) {
            keyMessages = append(keyMessages, msg)
        }
    }
    return keyMessages
}
```

**优点**：
- 计算速度快
- 可解释性强

**缺点**：
- 依赖规则质量，难以覆盖所有场景
- 语义理解能力有限

**适用场景**：特定领域的对话系统

---

### 2.3 LLM 增量摘要（Incremental Summarization）

**原理**：使用 LLM 对历史消息进行智能摘要，定期合并更新。

```
第一轮对话:
[用户] 我想预订明天下午的会议室
[助手] 好的，请问需要什么规格？
[用户] 10人左右，要有投影仪
[助手] 已为你预订会议室A
                              │
                              ▼
摘要: 用户想预订明天下午10人左右的会议室，要求有投影仪，已预订会议室A

第二轮对话:
[用户] 帮我改成后天上午10点
[助手] 已修改为后天上午10点
                              │
                              ▼
更新摘要: 用户想预订后天上午10人的会议室，要求有投影仪，已预订并修改为会议室A
```

**优点**：
- 智能理解语义，保留关键信息
- Token 压缩率高（通常 80%+）
- 保持上下文连贯性

**缺点**：
- 需要额外的 LLM 调用
- 摘要质量依赖 LLM 能力

**适用场景**：长对话、多轮交互、知识密集型对话

---

### 2.4 层次化摘要（Hierarchical Summarization）

**原理**：多级摘要，逐步提炼核心信息。

```
Level 1: [M1][M2] ──► S1=[摘要1-2]
         [M3][M4] ──► S2=[摘要3-4]
         [M5][M6] ──► S3=[摘要5-6]

Level 2: [S1][S2][S3] ──► Final=[整体摘要]
```

**优点**：
- 处理超长对话
- 多级抽象，信息分层

**缺点**：
- 实现复杂
- 多次 LLM 调用

**适用场景**：超长对话、文档分析、多轮会议记录

---

### 2.5 向量检索 RAG

**原理**：将历史消息向量化，按需检索相关上下文。

```
消息库: [M1 embedding][M2 embedding][M3 embedding]...
                │
                ▼
用户问题: "上次说的那个会议室..."
                │
                ▼
向量相似度检索 ──► 找到相关消息 ──► 拼接进上下文
```

**优点**：
- 精确检索，按需获取
- 可处理任意长度的历史

**缺点**：
- 需要 Embedding 模型
- 检索质量依赖向量模型

**适用场景**：知识密集型对话、需要精确回溯的场景

---

### 2.6 方案对比总结

| 方案 | 实现复杂度 | 上下文保留 | Token 效率 | 延迟 | 适用场景 |
|------|----------|-----------|-----------|------|---------|
| 滑动窗口 | ⭐ | ⭐⭐ | ⭐⭐⭐ | ⭐ | 简单对话 |
| 规则提取 | ⭐⭐ | ⭐⭐ | ⭐⭐ | ⭐⭐ | 特定领域 |
| LLM 增量摘要 | ⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐ | 长对话 |
| 层次化摘要 | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | 超长对话 |
| 向量检索 RAG | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐ | 知识密集型 |

---

## 3. 本项目实现方案

### 3.1 选择策略：LLM 增量摘要 + 缓存

**选择原因**：
1. 平衡实现复杂度和效果
2. 支持长对话场景而不丢失关键上下文
3. 自动适应不同对话长度
4. 可扩展性强，后续可升级为层次化摘要

### 3.2 架构设计

```
┌─────────────────────────────────────────────────────────────┐
│                    Memory 系统架构                          │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐     │
│  │   缓存层    │    │  摘要层     │    │  历史层     │     │
│  │   (Cache)   │    │ (Summary)   │    │ (History)   │     │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘     │
│         │                   │                   │            │
│         │   ┌───────────────┴───────────────┐   │            │
│         │   │                               │   │            │
│         ▼   ▼                               ▼   ▼            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │                  Optimizer 成本优化器                 │   │
│  └─────────────────────────────────────────────────────┘   │
│                            │                                │
│                            ▼                                │
│  ┌─────────────────────────────────────────────────────┐   │
│  │                   Runtime Agent                      │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### 3.3 核心流程

```
┌─────────────────────────────────────────────────────────────┐
│                    消息处理流程                             │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│   用户发送消息                                                │
│        │                                                     │
│        ▼                                                     │
│   ┌─────────────────┐                                        │
│   │ 检查相似问题缓存 │ ◄── 命中则直接返回，节省 Token          │
│   └────────┬────────┘                                        │
│            │ 未命中                                           │
│            ▼                                                 │
│   ┌─────────────────┐                                        │
│   │ 构建 LLM 请求   │                                        │
│   └────────┬────────┘                                        │
│            │                                                 │
│            ▼                                                 │
│   ┌─────────────────┐                                        │
│   │ 获取历史消息    │ ◄── 从 Summary 获取（可能已摘要）         │
│   └────────┬────────┘                                        │
│            │                                                 │
│            ▼                                                 │
│   ┌─────────────────┐                                        │
│   │ 调用 LLM        │                                        │
│   └────────┬────────┘                                        │
│            │                                                 │
│            ▼                                                 │
│   ┌─────────────────┐                                        │
│   │ 保存到历史      │                                        │
│   └────────┬────────┘                                        │
│            │                                                 │
│            ▼                                                 │
│   ┌─────────────────┐                                        │
│   │ 检查是否需要摘要 │                                        │
│   │ • Token 超限？  │                                        │
│   │ • 消息数超限？  │                                        │
│   └────────┬────────┘                                        │
│            │                                                 │
│            ▼                                                 │
│   ┌─────────────────┐                                        │
│   │ 执行 LLM 摘要   │ ──► 增量式摘要更新 currentSummary       │
│   └─────────────────┘                                        │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 4. 核心代码解析

### 4.1 文件结构

```
backend/internal/cost/
├── optimizer.go          # 成本优化器主文件
│   ├── Cache            # 相似问题缓存
│   ├── Summary          # 历史消息摘要
│   └── CalculateCost    # 成本计算
```

### 4.2 Summarizer 接口定义

```go
// Summarizer LLM摘要器接口
type Summarizer interface {
    // Summarize 对文本进行摘要
    Summarize(ctx context.Context, text string) (string, error)
    
    // IncrementalSummarize 增量式摘要（基于已有摘要 + 新内容）
    IncrementalSummarize(ctx context.Context, existingSummary, newContent string) (string, error)
    
    // EstimateTokens 估算 Token 数量
    EstimateTokens(text string) int
}

// 全局摘要器
var summarizer Summarizer

// SetSummarizer 设置摘要器
func SetSummarizer(s Summarizer) {
    summarizer = s
}
```

### 4.3 Summary 结构体

```go
type Summary struct {
    maxMessages    int       // 最大消息数量
    tokenLimit     int       // 最大 Token 数量（触发摘要阈值）
    history        []Message  // 消息历史
    currentSummary string    // 当前摘要内容
    mu             sync.RWMutex
}

type Message struct {
    Role      string   // 角色：user / assistant / system
    Content   string   // 内容
    IsSummary bool     // 是否为摘要消息
}
```

### 4.4 增量式摘要核心逻辑

```go
func (s *Summary) compressWithLLM() {
    // 1. 获取需要压缩的历史消息（保留最近2条作为上下文）
    recentCount := 2
    recentMessages := s.history[len(s.history)-recentCount:]
    messagesToSummarize := s.history[:len(s.history)-recentCount]

    // 2. 将消息转换为文本
    textToSummarize := s.messagesToText(messagesToSummarize)

    // 3. 调用 LLM 进行摘要
    var newSummary string
    var err error

    if s.currentSummary != "" {
        // 增量式更新：基于已有摘要 + 新内容
        newSummary, err = summarizer.IncrementalSummarize(
            context.Background(), 
            s.currentSummary, 
            textToSummarize,
        )
    } else {
        // 首次摘要
        newSummary, err = summarizer.Summarize(
            context.Background(), 
            textToSummarize,
        )
    }

    if err != nil {
        // LLM 摘要失败，使用降级方案
        s.compressFallback()
        return
    }

    // 4. 更新摘要
    s.currentSummary = newSummary

    // 5. 构建新的历史：摘要 + 最近消息
    summaryMsg := Message{
        Role:      "system",
        Content:   "[对话摘要] " + newSummary,
        IsSummary: true,
    }

    s.history = append([]Message{summaryMsg}, recentMessages...)
}
```

### 4.5 摘要触发条件

```go
func (s *Summary) Add(role, content string) {
    s.mu.Lock()
    defer s.mu.Unlock()

    s.history = append(s.history, Message{Role: role, Content: content})

    // 检查 Token 数量是否超过限制
    if s.tokenLimit > 0 && summarizer != nil {
        totalTokens := s.calculateTotalTokens()
        if totalTokens > s.tokenLimit {
            s.compressWithLLM()
            return
        }
    }

    // 检查消息数量是否超过阈值
    if len(s.history) >= s.maxMessages {
        s.compress()
    }
}
```

### 4.6 降级方案（简单滑动窗口）

```go
func (s *Summary) compressFallback() {
    if len(s.history) > 3 {
        summary := Message{
            Role:      "system",
            Content:   "之前进行了一些对话，以下是最近的对话内容。",
            IsSummary: true,
        }

        recent := s.history[len(s.history)-3:]
        s.history = append([]Message{summary}, recent...)
    }
}
```

---

## 5. 配置说明

### 5.1 配置文件（config.yaml）

```yaml
cost:
  max_history_messages: 10    # 最大消息数量
  max_history_tokens: 4096   # 最大 Token 数量（触发摘要阈值）
  summary_threshold: 10      # 摘要阈值（已废弃，使用 max_history_tokens）
  similarity_threshold: 0.95  # 相似度阈值
  cache_ttl: 3600s           # 缓存过期时间
```

### 5.2 配置说明

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `max_history_messages` | int | 10 | 消息数量阈值，超过则触发摘要 |
| `max_history_tokens` | int | 4096 | Token 数量阈值，超过则触发摘要 |
| `similarity_threshold` | float | 0.95 | 相似度匹配阈值（用于缓存命中） |
| `cache_ttl` | duration | 3600s | 缓存过期时间 |

### 5.3 建议配置

根据不同场景，建议配置如下：

```yaml
# 简单对话场景（节省成本）
cost:
  max_history_messages: 5
  max_history_tokens: 2048

# 标准对话场景（平衡效果）
cost:
  max_history_messages: 10
  max_history_tokens: 4096

# 长对话场景（保留更多上下文）
cost:
  max_history_messages: 20
  max_history_tokens: 8192
```

---

## 6. 扩展建议

### 6.1 启用 LLM 摘要功能

当前代码已支持 LLM 摘要框架，需要实现具体的 Summarizer：

```go
// 实现示例
type MySummarizer struct {
    llmAdapter llm.Adapter
}

func (s *MySummarizer) Summarize(ctx context.Context, text string) (string, error) {
    prompt := "请将以下对话历史总结成一段简洁的文字，保留关键信息：\n\n" + text
    resp, err := s.llmAdapter.Chat(ctx, &llm.LLMRequest{
        Messages: []llm.Message{{Role: "user", Content: prompt}},
        Model:    "glm-4-flash", // 使用便宜的模型
    })
    if err != nil {
        return "", err
    }
    return resp.Choices[0].Message.Content, nil
}

func (s *MySummarizer) IncrementalSummarize(ctx context.Context, existingSummary, newContent string) (string, error) {
    prompt := fmt.Sprintf(`已知摘要：%s

新增对话内容：
%s

请更新摘要，保留之前的关键信息，并添加新内容。`, existingSummary, newContent)
    
    resp, err := s.llmAdapter.Chat(ctx, &llm.LLMRequest{
        Messages: []llm.Message{{Role: "user", Content: prompt}},
        Model:    "glm-4-flash",
    })
    if err != nil {
        return "", err
    }
    return resp.Choices[0].Message.Content, nil
}

func (s *MySummarizer) EstimateTokens(text string) int {
    // 简单估算：中文按字符数/2，英文按单词数*1.3
    return len(text) / 2
}

// 在应用启动时注册
cost.SetSummarizer(&MySummarizer{llmAdapter: llmAdapter})
```

### 6.2 升级为层次化摘要

当对话非常长时，可以升级为层次化摘要：

```go
type HierarchicalSummarizer struct {
    summarizer Summarizer
    levelSize  int // 每层多少条消息
}

func (h *HierarchicalSummarizer) Summarize(ctx context.Context, text string) (string, error) {
    // Level 1: 将消息分成组，每组摘要
    groups := splitIntoGroups(messages, h.levelSize)
    level1Summaries := make([]string, 0)
    
    for _, group := range groups {
        summary, err := h.summarizer.Summarize(ctx, group)
        if err != nil {
            return "", err
        }
        level1Summaries = append(level1Summaries, summary)
    }
    
    // Level 2: 合并 Level 1 摘要
    if len(level1Summaries) > 1 {
        return h.summarizer.Summarize(ctx, join(level1Summaries))
    }
    
    return level1Summaries[0], nil
}
```

### 6.3 添加向量检索支持

对于需要精确回溯的场景，可以添加向量检索：

```go
type VectorMemory struct {
    cache      *Cache
    embeddingAPI EmbeddingAPI
    threshold  float64
}

func (v *VectorMemory) Add(ctx context.Context, role, content string) error {
    // 存储消息
    v.cache.Set(role, content)
    
    // 生成并存储向量
    embedding, err := v.embeddingAPI.GetEmbedding(ctx, content)
    if err != nil {
        return err
    }
    return v.storeEmbedding(content, embedding)
}

func (v *VectorMemory) Retrieve(ctx context.Context, query string) ([]Message, error) {
    queryEmbedding, err := v.embeddingAPI.GetEmbedding(ctx, query)
    if err != nil {
        return nil, err
    }
    
    // 相似度检索
    return v.findSimilar(ctx, queryEmbedding, v.threshold)
}
```

---

## 参考资料

- [MemGPT](https://github.com/MemGPT/MemGPT) - 大语言模型的智能记忆管理
- [斯坦福 ChatDB](https://arxiv.org/abs/2306.12602) - 将 SQL 数据库作为智能记忆
- [RAG vs Memory](https://www.pinecone.io/learn/contextual-retrieval/) - 上下文检索与记忆系统对比
- [Anthropic Context Window 处理](https://docs.anthropic.com/claude/docs/optimizing-llm-context) - 官方上下文优化指南

---

## 更新日志

| 日期 | 版本 | 更新内容 |
|------|------|---------|
| 2026-06-23 | v1.0 | 初始版本，实现 LLM 增量摘要框架 |
