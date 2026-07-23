package websocket

import (
	"encoding/json"
	"time"
)

// MessageType 消息类型
type MessageType string

const (
	// 客户端→服务器
	MessageTypeConnection  MessageType = "CONNECTION"
	MessageTypeChatMessage MessageType = "CHAT_MESSAGE"
	MessageTypePing        MessageType = "PING"

	// 服务器→客户端
	MessageTypeWelcome       MessageType = "WELCOME"
	MessageTypeNPCReply      MessageType = "NPC_REPLY"
	MessageTypeNPCReplyChunk MessageType = "NPC_REPLY_CHUNK" // 流式响应片段
	MessageTypeStreamEvent   MessageType = "STREAM_EVENT"    // 流式事件（透明推送所有事件）
	MessageTypeError         MessageType = "ERROR"
	MessageTypePong          MessageType = "PONG"
)

// Message WebSocket 消息
type Message struct {
	Type      MessageType `json:"type"`
	RequestID string      `json:"requestId"`
	TenantID  string      `json:"tenantId"`
	Timestamp int64       `json:"timestamp"`
	Payload   interface{} `json:"payload"`
}

// ConnectionPayload 连接负载
type ConnectionPayload struct {
	PlayerID string `json:"playerId"`
	Nickname string `json:"nickname"`
	DeviceID string `json:"deviceId"`
}

// ChatMessagePayload 聊天消息负载
type ChatMessagePayload struct {
	Message  string `json:"message"`
	PlayerID string `json:"playerId"`
}

// WelcomePayload 欢迎消息负载
type WelcomePayload struct {
	GuideName    string   `json:"guideName"`
	Message      string   `json:"message"`
	IsFirstVisit bool     `json:"isFirstVisit"`
	Tips         []string `json:"tips"`
	PlayerID     string   `json:"playerId"` // 后端生成的玩家ID
}

// NPCReplyPayload NPC回复负载
type NPCReplyPayload struct {
	GuideName string    `json:"guideName"`
	Message   string    `json:"message"`
	Emotion   string    `json:"emotion"`
	Actions   []string  `json:"actions"`
	Stats     *LLMStats `json:"stats,omitempty"`
}

// NPCReplyChunkPayload 流式NPC回复片段负载
type NPCReplyChunkPayload struct {
	GuideName    string  `json:"guideName"`
	Chunk        string  `json:"chunk"`   // 当前片段内容
	IsFinal      bool    `json:"isFinal"` // 是否是最后一个片段
	Emotion      string  `json:"emotion,omitempty"`
	Model        string  `json:"model,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	TotalTokens  int     `json:"totalTokens,omitempty"`
	Cost         float64 `json:"cost,omitempty"`
	LatencyMs    int64   `json:"latencyMs,omitempty"` // 耗时（毫秒）
}

// LLMStats LLM 调用统计信息
type LLMStats struct {
	Model        string  `json:"model"`
	LatencyMs    int64   `json:"latencyMs"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	TotalTokens  int     `json:"totalTokens"`
	Cost         float64 `json:"cost"`
	CacheHit     bool    `json:"cacheHit"`
}

// ErrorPayload 错误消息负载
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PongPayload 心跳响应负载
type PongPayload struct {
	ServerTime int64 `json:"serverTime"`
}

// ToolCall 工具调用信息
type ToolCall struct {
	ID       string                 `json:"id"`
	ToolName string                 `json:"tool_name"`
	Params   map[string]interface{} `json:"params"`
}

// StreamEventPayload 流式事件负载（透明推送所有Agent事件）
type StreamEventPayload struct {
	Type             string     `json:"type"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	IsThinking       bool       `json:"is_thinking,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolResult       string     `json:"tool_result,omitempty"`
	ActionType       string     `json:"action_type,omitempty"`
	Model            string     `json:"model,omitempty"`
	FinishReason     string     `json:"finish_reason,omitempty"`
	InputTokens      int        `json:"input_tokens,omitempty"`
	OutputTokens     int        `json:"output_tokens,omitempty"`
	TotalTokens      int        `json:"total_tokens,omitempty"`
	Cost             float64    `json:"cost,omitempty"`
	LatencyMs        int64      `json:"latency_ms,omitempty"`
}

// NewMessage 创建消息
func NewMessage(msgType MessageType, requestID, tenantID string, payload interface{}) (*Message, error) {
	return &Message{
		Type:      msgType,
		RequestID: requestID,
		TenantID:  tenantID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}, nil
}

// ParsePayload 解析负载
func (m *Message) ParsePayload(v interface{}) error {
	payloadBytes, err := json.Marshal(m.Payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(payloadBytes, v)
}

// String 返回消息的字符串表示
func (m *Message) String() string {
	data, _ := json.Marshal(m)
	return string(data)
}
