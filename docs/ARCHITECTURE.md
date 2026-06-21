# 系统架构文档

> 本文档从系统架构视角出发，详细说明江南水乡智能导游系统的整体架构、核心模块、关键流程与设计决策。


---

## 目录

- [1. 总体架构](#1-总体架构)
- [2. 分层架构](#2-分层架构)
- [3. 核心模块](#3-核心模块)
- [4. 关键流程图](#4-关键流程图)
- [5. 数据模型](#5-数据模型)
- [6. 设计决策](#6-设计决策)
- [7. 常见问题与设计 FAQ](#7-常见问题与设计-faq)


---

## 1. 总体架构

### 系统边界

```mermaid
graph LR
    subgraph 外部用户
        U[玩家/游客]
    end
    subgraph 系统
        F[Phaser 3 前端]
        B[Go 后端]
        DB[(MySQL)]
    end
    subgraph 外部服务
        LLM[多模型 LLM API]
        OBS[Prometheus / OpenTelemetry]
    end
    U --> F
    F -->|WebSocket| B
    B -->|SQL| DB
    B -->|HTTP/SSE| LLM
    B -->|Metrics/Trace| OBS
```

### 部署架构

```mermaid
graph TB
    subgraph 客户端
        A[浏览器]
    end
    subgraph 服务器
        B[Nginx 反向代理]
        C[前端静态文件]
        D[Go 后端服务]
        E[(MySQL)]
    end
    A -->|HTTPS / WebSocket| B
    B -->|/| C
    B -->|/ws| D
    D -->|TCP| E
```

---

## 2. 分层架构

### 后端分层

```mermaid
graph TB
    subgraph 接入层
        A[HTTP Server / Gin]
        B[WebSocket Handler]
        C[REST API / Health / Metrics]
    end
    subgraph 业务层
        D[Agent Runtime]
        E[Session Manager]
        F[Tool Registry]
        G[Emotion Detector]
        H[Cost Optimizer]
    end
    subgraph 模型层
        I[LLM Router]
        J[RouterAdapter]
        K[Provider 接口]
        L[Claude / OpenAI / 兼容]
    end
    subgraph 基础设施层
        M[Database]
        N[Knowledge Base]
        O[Logging]
        P[Observability]
    end
    A --> B
    A --> C
    B --> D
    D --> E
    D --> F
    D --> G
    D --> H
    D --> I
    I --> J
    J --> K
    K --> L
    D --> M
    F --> N
    A --> O
    D --> P
```

### 前端分层

```mermaid
graph TB
    subgraph 渲染层
        A[Phaser 3 游戏引擎]
        B[BootScene / WaterTownScene]
    end
    subgraph 实体层
        C[NPCGuide 小荷]
        D[Player 玩家]
        E[Background 背景]
    end
    subgraph UI 层
        F[DialogBox]
        G[InputBox]
        H[Typewriter]
    end
    subgraph 网络层
        I[WebSocketClient]
    end
    subgraph 配置层
        J[常量配置]
    end
    A --> B
    B --> C
    B --> D
    B --> E
    B --> F
    B --> G
    F --> H
    B --> I
    I --> J
```

---

## 3. 核心模块

### 3.1 WebSocket 模块

| 组件 | 职责 |
|------|------|
| `websocket.Hub` | 管理所有客户端连接、广播消息 |
| `websocket.Client` | 单个客户端连接、读写协程 |
| `WebSocketHandler` | 处理业务消息、连接生命周期 |
| `Message` | 统一消息协议 `{type, requestId, tenantId, timestamp, payload}` |

**特点**：
- 每个连接独立 `readPump` / `writePump` 协程
- 心跳保活（Ping/Pong）
- 断线自动重连（前端实现）
- 消息类型化：聊天、系统、错误、心跳

### 3.2 Agent Runtime

```mermaid
graph LR
    A[Agent Runtime] --> B[对话管理]
    A --> C[工具调用]
    A --> D[记忆管理]
    A --> E[情绪感知]
    A --> F[LLM 调用]
    B --> G[Session Manager]
    C --> H[Tool Registry]
    D --> I[Cost Optimizer]
    E --> J[Emotion Detector]
    F --> K[LLM Router]
```

### 3.3 多模型 LLM 路由

```mermaid
graph TB
    A[ChatRequest] --> B[Router]
    B --> C{Strategy}
    C -->|Fixed| D[固定模型]
    C -->|Cost| E[min cost]
    C -->|Latency| F[min EMA latency]
    C -->|Capability| G[任务分类 + 能力映射]
    C -->|Fallback| H[降级链]
    C -->|Weighted| I[按权重随机]
    D --> J[Provider]
    E --> J
    F --> J
    G --> J
    H --> J
    I --> J
    J --> K[Claude]
    J --> L[OpenAI]
    J --> M[GLM/Qwen]
```

---

## 4. 关键流程图

### 4.1 玩家发起一次对话的完整流程

```mermaid
sequenceDiagram
    autonumber
    participant P as 玩家
    participant F as 前端
    participant WS as WebSocket Handler
    participant SM as Session Manager
    participant RT as Agent Runtime
    participant ED as 情绪检测
    participant KB as 知识库
    participant LLM as LLM Router
    participant DB as MySQL

    P->>F: 输入并发送问题
    F->>WS: WS 消息 type=CHAT
    WS->>WS: 解析/校验消息
    WS->>SM: 获取或创建会话
    SM-->>WS: Session
    WS->>RT: 执行对话请求
    RT->>ED: 检测情绪
    ED-->>RT: 情绪标签
    RT->>KB: 检索相关知识/工具
    KB-->>RT: 知识片段
    RT->>RT: 构建 System Prompt + 上下文
    RT->>LLM: 发送 ChatRequest
    LLM->>LLM: 任务分类 + 策略选择
    LLM->>LLM: 选择 Provider + 降级重试
    LLM-->>RT: ChatResponse
    RT->>DB: 保存对话记录
    RT-->>WS: 返回回复
    WS->>F: WS 消息 type=MESSAGE
    F->>F: 打字机效果展示
    F->>P: 显示 NPC 回复
```

### 4.2 多模型路由选择流程

```mermaid
flowchart TD
    A[收到 ChatRequest] --> B[任务分类 ClassifyTask]
    B --> C{策略类型}
    C -->|Fixed| D[取 fixedModel]
    C -->|Cost| E[估算 token + 单价<br/>选择成本最低]
    C -->|Latency| F[读取 EMA 延迟<br/>选择最小]
    C -->|Capability| G[按 taskType 匹配能力映射]
    C -->|Fallback| H[遍历降级链]
    C -->|Weighted| I[按权重随机]
    D --> J{Provider 可用?}
    E --> J
    F --> J
    G --> J
    H --> J
    I --> J
    J -->|否| K[熔断检查 / 降级]
    K --> J
    J -->|是| L[调用 Provider.Chat]
    L --> M{成功?}
    M -->|否| N[记录错误 EMA]
    N --> K
    M -->|是| O[记录延迟 EMA]
    O --> P[返回 ChatResponse]
```

### 4.3 熔断器状态机

```mermaid
stateDiagram-v2
    [*] --> Closed: 正常启动
    Closed --> Open: 连续失败 >= threshold
    Open --> HalfOpen: 熔断时间到
    HalfOpen --> Closed: 探测成功
    HalfOpen --> Open: 探测失败
    Closed --> Closed: 成功/失败计数
```

### 4.4 成本优化流程

```mermaid
flowchart LR
    A[收到请求] --> B{相似问题缓存命中?}
    B -->|是| C[直接返回缓存]
    B -->|否| D[调用 LLM]
    D --> E{历史消息数 > threshold?}
    E -->|是| F[历史摘要压缩]
    E -->|否| G[保持原上下文]
    F --> H[发送请求]
    G --> H
    H --> I[记录 Token 消耗]
    I --> J[写入缓存]
```

---

## 5. 数据模型

### 核心数据库表

| 表名 | 用途 |
|------|------|
| `players` | 玩家信息 |
| `conversations` | 对话记录 |
| `audit_logs` | 审计日志（多租户） |

### 消息协议

```json
{
  "type": "CHAT",
  "requestId": "req_001",
  "tenantId": "tenant_001",
  "timestamp": 1718457600000,
  "payload": {
    "content": "苏州有什么好玩的？"
  }
}
```

### 回复协议

```json
{
  "type": "MESSAGE",
  "requestId": "req_001",
  "timestamp": 1718457601000,
  "payload": {
    "content": "苏州有拙政园、虎丘、平江路...",
    "emotion": "happy"
  }
}
```

---

## 6. 设计决策

### 6.1 为什么使用 WebSocket 而不是 HTTP 轮询？

| 维度 | WebSocket | HTTP 轮询 |
|------|-----------|-----------|
| 实时性 | 双向推送，毫秒级 | 依赖轮询间隔 |
| 资源消耗 | 长连接，低 overhead | 高频请求消耗大 |
| 游戏体验 | 支持 NPC 实时打字机效果 | 体验差 |

### 6.2 为什么自研多模型路由而不是使用 LangChain？

| 维度 | 自研路由 | LangChain |
|------|----------|-----------|
| 控制力 | 完全控制策略、降级、统计 | 受框架限制 |
| 性能 | 无额外抽象层，轻量 | 框架 overhead |
| 可观测 | 自定义 EMA、指标 | 通用指标 |
| 学习价值 | 深入理解模型调度 | 框架封装 |

### 6.3 为什么使用 EMA 而不是简单平均？

- **响应速度**：新样本权重 30%，能快速反映模型当前状态。
- **稳定性**：历史权重 70%，避免单次异常抖动影响决策。
- **内存高效**：无需保存所有历史数据，只维护一个 EMA 值。

### 6.4 为什么降级链优先于成本？

- **可用性优先**：用户请求失败比使用更贵的模型更糟糕。
- **渐进降级**：从高性能模型到低成本模型，平衡质量与成本。
- **容错能力**：单一模型故障不影响整体服务。

---

## 7. 常见问题与设计 FAQ


### Q1：项目最大的技术难点是什么？

**答**：多模型路由与降级机制。难点在于：
1. 如何抽象统一的 Provider 接口，兼容 Claude、OpenAI 和 OpenAI 兼容格式的 API；
2. 如何动态选择模型并保证可用性；
3. 如何统计运行时指标并用于路由决策。

我们设计了 `model.Provider` 接口、`Router` 策略引擎、`ModelStats` EMA 统计三层结构，实现了应用层与具体模型的解耦。

### Q2：如果某个模型挂了，系统怎么办？

**答**：
1. 熔断器检测到连续失败，进入 Open 状态，快速失败；
2. Fallback 策略按降级链尝试下一个模型；
3. 如果所有模型都失败，FallbackAdapter 返回预设兜底回复；
4. 同时 EMA 错误率更新，高错误模型被快速降级。

### Q3：如何控制 API 成本？

**答**：
1. 成本优先策略自动选择单价最低的模型；
2. 相似问题缓存命中直接返回；
3. 历史消息超过阈值自动摘要，减少 token；
4. 本地 Token 估算，无需调用 API 即可预估成本。

### Q4：前后端如何保持状态？

**答**：
- WebSocket 长连接维持玩家会话；
- Session Manager 管理每个玩家的对话上下文；
- MySQL 持久化对话记录；
- 前端断开重连后通过 requestId / playerId 恢复会话。

### Q5：项目的可扩展性如何？

**答**：
- 新增 Provider：实现 `model.Provider` 接口即可；
- 新增路由策略：在 `router` 中添加策略分支；
- 新增工具：实现 `agent.Tool` 接口并注册到 `ToolRegistry`；
- 前端新增场景：新增 Phaser Scene 即可。

---

## 附录：技术栈

| 层级 | 技术 |
|------|------|
| 后端语言 | Go 1.25+ |
| HTTP 框架 | Gin |
| WebSocket | Gorilla WebSocket |
| 数据库 | MySQL 8.0 |
| ORM | GORM |
| LLM SDK | anthropic-sdk-go, go-openai |
| 可观测 | Prometheus, OpenTelemetry |
| 前端引擎 | Phaser 3 |
| 部署 | Docker, Render |

---

*本文档用于项目介绍和技术分享，建议结合代码和流程图进行讲解。*

