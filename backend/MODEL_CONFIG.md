# 多模型配置说明

## 概述

系统基于 **Eino 框架**构建多模型调用能力，所有模型统一通过 OpenAI 兼容接口接入。通过配置文件管理多个模型，支持 6 种路由策略和自动降级机制。

## 快速配置

### YAML 配置（推荐）

编辑 `configs/config.yaml`：

```yaml
llm:
  # 模型列表，按优先级排列
  models:
    # 小米模型（首选）
    - name: mimo-v2.5
      base_url: https://token-plan-cn.xiaomimimo.com/v1
      api_key: ${MIMO_API_KEY}
      enabled: true
      max_tokens: 300
      temperature: 0.5

    # 阿里云通义千问（备选）
    - name: qwen3.5-27b
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
      api_key: ${BAILIAN_API_KEY}
      enabled: true
      max_tokens: 500
      temperature: 0.7

  # 通用配置
  timeout: 60s        # 请求超时
  max_retries: 3      # 失败重试次数
  retry_delay: 2s     # 重试延迟
  auto_switch: true   # 启用降级链自动切换
  strategy: capability  # 路由策略: fixed/cost/latency/capability/fallback/weighted
```

### 重要变化（迁移到 Eino 后）

| 旧配置 | 新配置 | 说明 |
|-------|-------|------|
| `type: claude` | ❌ 删除 | 所有模型统一通过 OpenAI 兼容接口接入 |
| `mode: auto` | ❌ 删除 | 流式/非流式由调用方法决定 |
| `base_url: .../anthropic` | `base_url: .../v1` | 改为 OpenAI 兼容端点 |

## 配置参数

### 模型配置

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | 是 | 模型名称，用于 API 调用和日志标识 |
| `base_url` | string | 是 | OpenAI 兼容 API 地址（需支持 `/v1/chat/completions` 格式） |
| `api_key` | string | 是 | API Key，支持 `${ENV_VAR}` 环境变量替换 |
| `enabled` | bool | 是 | 是否启用该模型 |
| `max_tokens` | int | 否 | 最大生成 token 数，默认 300 |
| `temperature` | float | 否 | 生成温度，默认 0.7 |

### 通用配置

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `timeout` | duration | 否 | 请求超时，默认 60s |
| `max_retries` | int | 否 | 重试次数，默认 3 |
| `retry_delay` | duration | 否 | 重试延迟，默认 2s |
| `auto_switch` | bool | 否 | 是否启用降级自动切换，默认 true |
| `strategy` | string | 否 | 路由策略，默认 fallback |

## 环境变量

### 不同模型的 API Key

```bash
# 小米模型
export MIMO_API_KEY="your-mimo-key"

# 阿里云通义千问
export BAILIAN_API_KEY="your-bailian-key"

# 智谱 GLM
export GLM_API_KEY="your-glm-key"

# OpenAI
export OPENAI_API_KEY="your-openai-key"

# Anthropic Claude
export ANTHROPIC_API_KEY="your-anthropic-key"
```

### Windows PowerShell

```powershell
$env:MIMO_API_KEY = "your-key"
$env:BAILIAN_API_KEY = "your-key"
```

## 路由策略

系统支持 6 种路由策略，通过配置文件的 `strategy` 字段指定：

### 1. Fallback（降级链）— 生产推荐

```yaml
strategy: fallback
```

按配置顺序依次尝试模型，失败后自动切换到下一个。

**适用场景**：生产环境，保证高可用性。

### 2. Cost（成本优先）

```yaml
strategy: cost
```

选择成本最低的模型（需扩展价格配置）。

**适用场景**：成本敏感场景。

### 3. Latency（延迟优先）

```yaml
strategy: latency
```

根据 EMA 统计选择延迟最低的模型。

**适用场景**：实时对话，要求快速响应。

### 4. Capability（能力优先）

```yaml
strategy: capability
```

根据消息内容自动分类任务类型，选择最适合的模型。

**适用场景**：混合场景，不同任务类型有不同的最优模型。

**任务分类优先级**：Code > Reasoning > Chinese > LongText > General。

### 5. Weighted（加权）

```yaml
strategy: weighted
```

按权重随机选择模型，适合 A/B 测试或流量分配。

**适用场景**：A/B 测试、灰度发布。

### 6. Fixed（固定）

```yaml
strategy: fixed
```

始终使用配置中的第一个模型。

**适用场景**：开发调试、单模型测试。

## 支持的模型接入方式

所有模型通过 **OpenAI 兼容接口**接入，无需区分 Provider 类型。

### 小米模型

```yaml
- name: mimo-v2.5
  base_url: https://token-plan-cn.xiaomimimo.com/v1
  api_key: ${MIMO_API_KEY}
  enabled: true
  max_tokens: 300
  temperature: 0.5
```

### 阿里云通义千问

```yaml
- name: qwen3.5-27b
  base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
  api_key: ${BAILIAN_API_KEY}
  enabled: true
  max_tokens: 500
  temperature: 0.7
```

### 智谱 GLM

```yaml
- name: glm-4-flash
  base_url: https://open.bigmodel.cn/api/paas/v4
  api_key: ${GLM_API_KEY}
  enabled: true
  max_tokens: 300
  temperature: 0.7
```

### OpenAI GPT

```yaml
- name: gpt-4o
  base_url: https://api.openai.com/v1
  api_key: ${OPENAI_API_KEY}
  enabled: true
  max_tokens: 2000
  temperature: 0.7
```

### Anthropic Claude

```yaml
- name: claude-3.5-sonnet
  base_url: https://api.anthropic.com/v1
  api_key: ${ANTHROPIC_API_KEY}
  enabled: true
  max_tokens: 2000
  temperature: 0.7
```

### DeepSeek / 其他 OpenAI 兼容 API

只要 API 支持 OpenAI Chat Completions 格式（`/v1/chat/completions`），都可以通过设置 `base_url` 接入。

```yaml
- name: deepseek-chat
  base_url: https://api.deepseek.com/v1
  api_key: ${DEEPSEEK_API_KEY}
  enabled: true
  max_tokens: 500
  temperature: 0.7
```

## 流式与非流式调用

### 自动切换

调用方法决定模式，**无需配置**：

| 调用方法 | Eino 行为 | HTTP 请求 |
|---------|----------|----------|
| `Chat()` | 非流式 | `stream=false` |
| `StreamChat()` | 流式 | `stream=true` |

**一个 BaseURL，两种模式**：不需要区分"流式 URL"和"非流式 URL"，Eino 自动处理。

### 流式降级

当流式调用失败且未返回任何内容时，系统自动降级为非流式调用：

```
流式失败（无内容） → 非流式重试 → 成功返回
```

**降级原因**：某些模型的流式端点可能不稳定，但非流式端点正常。

## 自动切换机制

### 触发条件

1. **API 请求失败** — 网络错误、超时、API 返回错误
2. **空响应** — 模型返回空内容
3. **连续失败** — 同一模型连续失败达到阈值

### 切换逻辑（Eino ModelFailover）

```
1. 根据路由策略选择主模型
2. 如果失败：
   → ShouldFailover 判断是否需要降级
   → GetFailoverModel 返回下一个模型
3. 继续尝试，直到成功或达到 MaxRetries
4. 所有模型失败：返回 FallbackAdapter 预设回复
```

### 日志示例

```json
{"level":"info","msg":"Eino model registered","name":"mimo-v2.5","model":"mimo-v2.5","base_url":"https://token-plan-cn.xiaomimimo.com/v1"}
{"level":"info","msg":"Eino model registered","name":"qwen3.5-27b","model":"qwen3.5-27b","base_url":"https://dashscope.aliyuncs.com/compatible-mode/v1"}
{"level":"error","msg":"Model call failed","model":"mimo-v2.5","error":"rate limit exceeded"}
{"level":"info","msg":"Failover to next model","from":"mimo-v2.5","to":"qwen3.5-27b"}
```

## EMA 统计详情

系统为每个模型维护运行时统计，用于 `Latency` 和 `Weighted` 策略的决策：

```
延迟 EMA：newEMA = 0.3 × currentSample + 0.7 × previousEMA
错误率 EMA：newEMA = 0.3 × (err ? 1.0 : 0.0) + 0.7 × previousEMA
综合评分：Score = Latency + ErrorRate × 10000
```

**设计意图**：
- 错误率放大权重，确保高错误模型被快速降级
- 30% 新样本权重保证快速响应模型状态变化
- 70% 历史权重保证稳定性

## 常见问题

### Q1: 如何添加新模型？

在 `configs/config.yaml` 的 `llm.models` 中添加：

```yaml
- name: your-model
  base_url: https://your-api.com/v1  # 必须支持 OpenAI Chat Completions 格式
  api_key: ${YOUR_API_KEY}
  enabled: true
  max_tokens: 300
  temperature: 0.7
```

**注意**：不再需要 `type` 字段，所有模型统一接入。

### Q2: 如何切换路由策略？

修改配置文件中的 `strategy` 字段：

```yaml
strategy: fallback  # 或 cost/latency/capability/weighted/fixed
```

### Q3: 如何禁用某个模型？

将 `enabled` 设为 `false`：

```yaml
- name: glm-4
  enabled: false
```

### Q4: 如何调整模型优先级？

调整模型在配置文件中的顺序 — Fallback 策略下，越靠前的模型优先级越高。

### Q5: 所有模型都失败了怎么办？

依次尝试：
1. 流式降级为非流式（同模型）
2. ModelFailover 切换到下一个模型
3. FallbackAdapter 返回预设兜底回复

### Q6: 流式调用会自动启用吗？

是的，调用 `StreamChat()` 时 Eino 自动发送 `stream=true` 参数，解析 SSE 响应。

### Q7: Claude 模型如何接入？

使用 Anthropic 的 OpenAI 兼容端点（需确认是否支持），或通过代理服务转换。

## 注意事项

1. **API Key 安全**：不要直接写在配置文件中，使用环境变量 `${VAR_NAME}`
2. **API 兼容性**：确保 `base_url` 支持 OpenAI Chat Completions 格式
3. **配置简化**：不再需要 `type` 和 `mode` 字段
4. **测试验证**：添加新模型后先测试，确认 API 格式兼容
5. **流式支持**：调用 `StreamChat()` 时自动启用流式，无需额外配置

## 技术栈变化

| 旧技术栈 | 新技术栈 |
|---------|---------|
| `anthropic-sdk-go` | ❌ 删除 |
| `go-openai` | ❌ 删除 |
| 自研 Provider 接口 | ❌ 删除 |
| 自研降级链 | Eino ModelFailoverConfig |
| 自研重试机制 | Eino ModelRetryConfig |
| - | `github.com/cloudwego/eino` |
| - | `github.com/cloudwego/eino-ext/components/model/openai` |

---

*本文档描述 Eino 框架迁移后的多模型配置方式。*