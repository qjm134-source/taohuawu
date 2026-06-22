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
	MessageTypeError         MessageType = "ERROR"
	MessageTypePong          MessageType = "PONG"
)

// Message WebSocket 消息
type Message struct {
	Type      MessageType     `json:"type"`
	RequestID string          `json:"requestId"`
	TenantID  string          `json:"tenantId"`
	Timestamp int64           `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
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

// NewMessage 创建消息
func NewMessage(msgType MessageType, requestID, tenantID string, payload interface{}) (*Message, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return &Message{
		Type:      msgType,
		RequestID: requestID,
		TenantID:  tenantID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payloadBytes,
	}, nil
}

// ParsePayload 解析负载
func (m *Message) ParsePayload(v interface{}) error {
	return json.Unmarshal(m.Payload, v)
}

// String 返回消息的字符串表示
func (m *Message) String() string {
	data, _ := json.Marshal(m)
	return string(data)
}
