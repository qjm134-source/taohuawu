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

### 3.3 多模型 LLM 路由（基于 Eino ReAct Agent）

```mermaid
graph TB
    A[ChatRequest] --> B[EinoAdapter]
    B --> C[Eino ReAct Agent]
    C --> D{ModelFailoverConfig}
    D --> E[ShouldFailover判断]
    E --> F{需要降级?}
    F -->|是| G[GetFailoverModel选择]
    F -->|否| H[返回响应]
    G --> I{路由策略}
    I -->|Fixed| J[固定模型]
    I -->|Cost| K[min cost]
    I -->|Latency| L[min EMA latency]
    I -->|Capability| M[任务分类+能力映射]
    I -->|Fallback| N[降级链下一个]
    I -->|Weighted| O[按权重随机]
    J --> P[Eino OpenAI ChatModel]
    K --> P
    L --> P
    M --> P
    N --> P
    O --> P
    P --> Q[OpenAI兼容API]
    Q --> R[小米/阿里/智谱/OpenAI/Claude]
    
    C -.-> S[工具自动调用循环]
    S --> S1[Reason: 判断是否调用工具]
    S1 --> S2[Act: 执行工具]
    S2 --> S3[Observe: 获取结果]
    S3 --> S4[Respond: 返回最终回复]
```

**关键变化**：使用 `eino_react.Agent` 替代原有的 `eino_adk.ChatModelAgent`，工具调用循环由 Agent 内部自动完成，无需应用层干预。

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

### 4.2 多模型路由选择流程（Eino）

```mermaid
flowchart TD
    A[收到 ChatRequest] --> B[EinoAdapter处理]
    B --> C[Eino ChatModelAgent]
    C --> D{路由策略选择主模型}
    D -->|Fixed| E[固定模型]
    D -->|Cost| F[成本最低模型]
    D -->|Latency| G[EMA延迟最低]
    D -->|Capability| H[任务分类匹配]
    D -->|Fallback| I[配置顺序首个]
    D -->|Weighted| J[权重随机]
    E --> K[Eino OpenAI ChatModel调用]
    F --> K
    G --> K
    H --> K
    I --> K
    J --> K
    K --> L{调用成功?}
    L -->|是| M[更新EMA统计]
    L -->|否| N[ModelFailover判断]
    N --> O{需要降级?}
    O -->|是| P[选择下一个模型]
    O -->|否| Q[返回错误]
    P --> K
    M --> R[返回ChatResponse]
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

### 6.2 为什么使用 Eino ReAct Agent 而不是自研路由？

| 维度 | Eino ReAct Agent | 自研路由（旧方案） |
|------|-----------------|-------------------|
| **维护成本** | 框架维护，跟随社区更新 | 自行维护所有 Provider + ReAct 循环 |
| **抽象层级** | 统一 `ChatModel` 接口 + `react.Agent` | 自定义 `Provider` 接口 + 手动工具调用 |
| **工具调用** | Agent 内部自动完成 Reason→Act→Observe→Respond | 应用层手动解析 tool_calls、执行工具、注入结果 |
| **故障转移** | 内置 `ModelFailoverConfig` | 手动实现降级链 |
| **重试机制** | 内置 `ModelRetryConfig` | 手动实现重试逻辑 |
| **流式处理** | 自动处理 SSE 流，支持流式 ReAct | 手动解析 SSE 数据 |
| **Tool Calling** | 统一工具注册，自动匹配执行 | 各 Provider 不同实现 |
| **可观测性** | 内置 Callbacks 机制，支持 trace/audit | 应用层手动埋点 |
| **扩展性** | 通过 OpenAI 兼容接口接入任意模型 | 需为每个 Provider 写适配器 |

**迁移收益**：
- **减少代码量**：移除了大量手动处理工具调用的代码（`runWithToolLoop`、`runStreamWithToolLoop` 等）
- **工具自动执行**：ReAct Agent 内部自动完成"思考→调用工具→获取结果→继续思考"的完整循环
- **统一抽象**：所有模型通过同一 OpenAI 兼容接口接入
- **内置可靠性**：Eino 提供故障转移和重试机制
- **回调追踪**：通过 `callbacks.Handler` 实现模型调用和工具调用的 trace 与审计日志
- **简化配置**：不需要区分 Provider 类型（`type` 字段已删除）

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
1. 如何抽象统一的模型接口，兼容不同 API 格式；
2. 如何动态选择模型并保证可用性；
3. 如何统计运行时指标并用于路由决策。

我们通过 **Eino 框架** 解决了这些问题：
- 统一使用 OpenAI 兼容接口接入所有模型
- `ModelFailoverConfig` 提供内置故障转移机制
- 自研 EMA 统计用于路由策略决策

### Q2：如果某个模型挂了，系统怎么办？

**答**：
1. Eino `ModelFailoverConfig` 检测失败，触发降级；
2. `GetFailoverModel` 根据策略返回下一个模型；
3. 如果所有模型都失败，FallbackAdapter 返回预设兜底回复；
4. 同时 EMA 错误率更新，高错误模型在 `Latency` 策略下被优先降级。

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
- 新增模型：在配置文件中添加，支持 OpenAI Chat Completions 格式即可；
- 新增路由策略：在 `multi_model_adapter.go` 中添加策略分支；
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
| LLM 框架 | Eino (CloudWeGo) |
| ChatModel | Eino OpenAI ChatModel |
| 可观测 | Prometheus, OpenTelemetry |
| 前端引擎 | Phaser 3 |
| 部署 | Docker, Render |

---

*本文档用于项目介绍和技术分享，建议结合代码和流程图进行讲解。*

