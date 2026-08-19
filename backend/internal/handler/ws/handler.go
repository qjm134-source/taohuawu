package ws

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/watertown/guide/internal/adapter/llm"
	"github.com/watertown/guide/internal/core/agent"
	"github.com/watertown/guide/internal/core/session"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/internal/repository"
	"github.com/watertown/guide/pkg/logging"
	"github.com/watertown/guide/pkg/utils"
)

// WebSocketHandler WebSocket 处理器
type WebSocketHandler struct {
	hub            *Hub
	sessionManager *session.SessionManager
	runtime        *agent.Runtime
	playerRepo     repository.PlayerRepository
	convRepo       repository.ConversationRepository
	auditRepo      repository.AuditRepository
	logger         logging.Logger
}

// NewWebSocketHandler 创建 WebSocket 处理器
func NewWebSocketHandler(
	hub *Hub,
	sessionManager *session.SessionManager,
	runtime *agent.Runtime,
	playerRepo repository.PlayerRepository,
	convRepo repository.ConversationRepository,
	auditRepo repository.AuditRepository,
	logger logging.Logger,
) *WebSocketHandler {
	return &WebSocketHandler{
		hub:            hub,
		sessionManager: sessionManager,
		runtime:        runtime,
		playerRepo:     playerRepo,
		convRepo:       convRepo,
		auditRepo:      auditRepo,
		logger:         logger,
	}
}

// Handle 处理 WebSocket 连接
func (h *WebSocketHandler) Handle(c *gin.Context) {
	// 升级 HTTP 连接为 WebSocket 连接
	conn, err := Upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Error("WebSocket upgrade failed", "error", err)
		return
	}

	// 创建客户端（暂不注册，等收到 CONNECTION 消息后再注册以完成去重）
	client := NewClient(conn, "", "", nil)

	// 先启动读写泵
	go func() {
		defer utils.RecoverWithCustomLogger("WritePump", h.logger)
		client.WritePump()
	}()
	go func() {
		defer utils.RecoverWithCustomLogger("ReadPump", h.logger)
		client.ReadPump(h.hub, h.handleMessage)
	}()
}

// handleMessage 处理消息
func (h *WebSocketHandler) handleMessage(client *Client, message []byte) {
	// 解析消息
	var msg Message
	if err := json.Unmarshal(message, &msg); err != nil {
		h.logger.Error("Failed to parse message", "error", err)
		return
	}

	// 记录接收消息指标
	observability.WebSocketMessagesTotal.WithLabelValues(string(msg.Type), "in").Inc()

	switch msg.Type {
	case MessageTypeConnection:
		h.handleConnection(client, &msg)
	case MessageTypeChatMessage:
		h.handleChatMessage(client, &msg)
	case MessageTypePing:
		h.handlePing(client, &msg)
	default:
		h.logger.Warn("Unknown message type", "type", msg.Type)
	}
}

// handleConnection 处理连接消息
func (h *WebSocketHandler) handleConnection(client *Client, msg *Message) {
	payload, ok := h.parseConnectionPayload(client, msg)
	if !ok {
		return
	}

	h.registerClient(client, msg, payload)
	if !client.IsValid() {
		observability.WebSocketConnections.WithLabelValues(msg.TenantID).Dec()
		return
	}

	player, isNewPlayer, ok := h.ensurePlayer(payload, msg)
	if !ok || !client.IsValid() {
		return
	}

	h.sendWelcome(client, msg, player, isNewPlayer)
}

func (h *WebSocketHandler) parseConnectionPayload(client *Client, msg *Message) (ConnectionPayload, bool) {
	var payload ConnectionPayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse connection payload", "error", err)
		return payload, false
	}
	client.TenantID = msg.TenantID
	client.PlayerID = payload.PlayerID
	return payload, true
}

func (h *WebSocketHandler) registerClient(client *Client, msg *Message, payload ConnectionPayload) {
	h.hub.Register <- client
	observability.WebSocketConnections.WithLabelValues(msg.TenantID).Inc()
}

func (h *WebSocketHandler) ensurePlayer(payload ConnectionPayload, msg *Message) (*repository.Player, bool, bool) {
	player, err := h.playerRepo.GetByDeviceID(payload.DeviceID, msg.TenantID)
	if err != nil {
		return h.createPlayer(payload, msg)
	}

	if err := h.playerRepo.UpdateLastVisit(player.ID); err != nil {
		h.logger.Error("Failed to update last visit", "error", err, "player_id", player.ID)
	}
	return player, false, true
}

func (h *WebSocketHandler) createPlayer(payload ConnectionPayload, msg *Message) (*repository.Player, bool, bool) {
	player := &repository.Player{
		ID:             uuid.New().String(),
		TenantID:       msg.TenantID,
		Nickname:       payload.Nickname,
		DeviceID:       payload.DeviceID,
		FirstVisitTime: time.Now(),
		LastVisitTime:  time.Now(),
		TotalDialogues: 0,
	}
	if err := h.playerRepo.Create(player); err != nil {
		h.logger.Error("Failed to create player", "error", err)
		return nil, false, false
	}
	return player, true, true
}

func (h *WebSocketHandler) sendWelcome(client *Client, msg *Message, player *repository.Player, isNewPlayer bool) {
	sess := h.runtime.GetSession(player.ID, msg.TenantID)
	sess.Nickname = player.Nickname

	reply, err := h.runtime.HandleWelcome(context.Background(), sess)
	if err != nil {
		h.logger.Error("Failed to handle welcome", "error", err)
		reply = "欢迎来到江南水乡！我是导游小荷，很高兴为你服务。"
	}

	h.runtime.MarkVisited(sess.ID)

	welcomeMsg, err := NewMessage(
		MessageTypeWelcome,
		msg.RequestID,
		msg.TenantID,
		WelcomePayload{
			GuideName:    agent.GuideName,
			Message:      reply,
			IsFirstVisit: isNewPlayer,
			Tips:         []string{"点击输入框与小荷对话", "可以问我关于游戏的问题"},
			PlayerID:     player.ID,
		},
	)
	if err != nil {
		h.logger.Error("Failed to create welcome message", "error", err)
		return
	}

	if err := client.SendMessage(welcomeMsg); err != nil {
		h.logger.Error("Failed to send welcome message", "error", err)
	}
}

// handleChatMessage 处理聊天消息
func (h *WebSocketHandler) handleChatMessage(client *Client, msg *Message) {
	payload, ok := h.parseChatPayload(client, msg)
	if !ok {
		return
	}

	player, ok := h.ensureChatPlayer(client, msg, payload)
	if !ok {
		return
	}

	session := h.runtime.GetSession(player.ID, msg.TenantID)
	eventChan, statsChan, err := h.runtime.HandleChatStream(context.Background(), session, payload.Message)
	if err != nil {
		h.logger.Error("Failed to handle chat stream", "error", err, "player_id", player.ID)
		h.sendError(client, msg, "CHAT_ERROR", "抱歉，我现在无法回答你的问题。请稍后再试。")
		return
	}

	fullReply, stats := h.streamResponse(client, msg, eventChan, statsChan)
	h.persistChatResult(session, player, msg, payload.Message, fullReply, stats)
}

func (h *WebSocketHandler) parseChatPayload(client *Client, msg *Message) (ChatMessagePayload, bool) {
	var payload ChatMessagePayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse chat payload", "error", err)
		return payload, false
	}
	return payload, true
}

func (h *WebSocketHandler) ensureChatPlayer(client *Client, msg *Message, payload ChatMessagePayload) (*repository.Player, bool) {
	player, err := h.playerRepo.GetByID(payload.PlayerID)
	if err == nil {
		return player, true
	}

	h.logger.Warn("Player not found by ID, trying to find by deviceId", "player_id", payload.PlayerID)
	player, err = h.playerRepo.GetByDeviceID(client.ID, msg.TenantID)
	if err == nil {
		return player, true
	}

	return h.createChatPlayer(client, msg)
}

func (h *WebSocketHandler) createChatPlayer(client *Client, msg *Message) (*repository.Player, bool) {
	player := &repository.Player{
		ID:             uuid.New().String(),
		TenantID:       msg.TenantID,
		Nickname:       "游客",
		DeviceID:       client.ID,
		FirstVisitTime: time.Now(),
		LastVisitTime:  time.Now(),
		TotalDialogues: 0,
	}
	if err := h.playerRepo.Create(player); err != nil {
		h.logger.Error("Failed to create player", "error", err)
		h.sendError(client, msg, "PLAYER_CREATE_ERROR", "无法创建玩家信息，请重试。")
		return nil, false
	}
	return player, true
}

func (h *WebSocketHandler) sendError(client *Client, msg *Message, code, message string) {
	errMsg, err := NewMessage(
		MessageTypeError,
		msg.RequestID,
		msg.TenantID,
		ErrorPayload{
			Code:    code,
			Message: message,
		},
	)
	if err != nil {
		h.logger.Error("Failed to create error message", "error", err)
		return
	}
	if err := client.SendMessage(errMsg); err != nil {
		h.logger.Error("Failed to send error message", "error", err)
	}
}

func (h *WebSocketHandler) streamResponse(client *Client, msg *Message,
	eventChan <-chan *llm.StreamEvent, statsChan <-chan *agent.LLMStats) (string, *agent.LLMStats) {

	var fullReply strings.Builder
	for event := range eventChan {
		if event.Type == llm.StreamEventTypeChunk && event.Content != "" {
			fullReply.WriteString(event.Content)
		}

		if event.Content == "" && event.ReasoningContent == "" && len(event.ToolCalls) == 0 && event.FinishReason == "" {
			continue
		}

		h.sendStreamEvent(client, msg, event)
	}

	stats := <-statsChan
	h.sendCompleteEvent(client, msg, stats)
	return fullReply.String(), stats
}

func (h *WebSocketHandler) sendStreamEvent(client *Client, msg *Message, event *llm.StreamEvent) {
	toolCalls := make([]ToolCall, 0, len(event.ToolCalls))
	for _, tc := range event.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID:       tc.ID,
			ToolName: tc.ToolName,
			Params:   tc.Params,
		})
	}

	eventMsg, err := NewMessage(
		MessageTypeStreamEvent,
		msg.RequestID,
		msg.TenantID,
		StreamEventPayload{
			Type:             string(event.Type),
			Content:          event.Content,
			ReasoningContent: event.ReasoningContent,
			IsThinking:       event.IsThinking,
			ToolCalls:        toolCalls,
			ToolResult:       event.ToolResult,
			ActionType:       event.ActionType,
			Model:            event.Model,
			FinishReason:     event.FinishReason,
		},
	)
	if err != nil {
		h.logger.Error("[WebSocket] Failed to create message", "error", err)
		return
	}
	if err := client.SendMessage(eventMsg); err != nil {
		h.logger.Error("[WebSocket] Failed to send message", "error", err)
	}
}

func (h *WebSocketHandler) sendCompleteEvent(client *Client, msg *Message, stats *agent.LLMStats) {
	completeMsg, err := NewMessage(
		MessageTypeStreamEvent,
		msg.RequestID,
		msg.TenantID,
		StreamEventPayload{
			Type:         string(llm.StreamEventTypeChunk),
			Content:      "",
			FinishReason: "stop",
			Model:        stats.Model,
			InputTokens:  stats.InputTokens,
			OutputTokens: stats.OutputTokens,
			TotalTokens:  stats.TotalTokens,
			Cost:         stats.Cost,
			LatencyMs:    stats.LatencyMs,
		},
	)
	if err != nil {
		h.logger.Error("Failed to create complete message", "error", err)
		return
	}
	if err := client.SendMessage(completeMsg); err != nil {
		h.logger.Error("Failed to send complete message", "error", err)
	}
}

func (h *WebSocketHandler) persistChatResult(sess *session.Session, player *repository.Player, msg *Message,
	userMessage, reply string, stats *agent.LLMStats) {

	if err := h.playerRepo.IncrementDialogues(player.ID); err != nil {
		h.logger.Error("Failed to increment dialogues", "error", err, "player_id", player.ID)
	}

	h.saveConversation(sess, player, msg, userMessage, reply, stats)
	h.saveAuditLog(sess, player, msg, userMessage, reply, stats)

	observability.WebSocketMessagesTotal.WithLabelValues(string(MessageTypeStreamEvent), "out").Inc()
}

func (h *WebSocketHandler) saveConversation(sess *session.Session, player *repository.Player, msg *Message,
	userMessage, reply string, stats *agent.LLMStats) {

	conv := &repository.Conversation{
		ID:          uuid.New().String(),
		PlayerID:    player.ID,
		TenantID:    msg.TenantID,
		SessionID:   sess.ID,
		UserMessage: userMessage,
		AIMessage:   reply,
		Emotion:     stats.Model, // 临时使用，实际应该从上下文获取
		ToolsUsed:   repository.JSON{Data: stats.ToolsUsed},
		LLMModel:    stats.Model,
		LLMTokens:   stats.TotalTokens,
		Cost:        stats.Cost,
		CacheHit:    stats.CacheHit,
		CreatedAt:   time.Now(),
	}
	if err := h.convRepo.Create(conv); err != nil {
		h.logger.Error("Failed to create conversation", "error", err)
	}
}

func (h *WebSocketHandler) saveAuditLog(sess *session.Session, player *repository.Player, msg *Message,
	userMessage, reply string, stats *agent.LLMStats) {

	if h.auditRepo == nil {
		h.logger.Error("auditRepo is nil, cannot create audit log")
		return
	}

	auditLog := &repository.AuditLog{
		ID:             uuid.New().String(),
		TenantID:       msg.TenantID,
		PlayerID:       player.ID,
		Action:         "chat",
		RequestPayload: repository.JSON{Data: map[string]string{"message": userMessage}},
		ResponsePayload: repository.JSON{Data: map[string]interface{}{
			"reply":       reply,
			"model":       stats.Model,
			"totalTokens": stats.TotalTokens,
			"latencyMs":   stats.LatencyMs,
			"cost":        stats.Cost,
			"cacheHit":    stats.CacheHit,
		}},
		Status:    "success",
		LatencyMs: int(stats.LatencyMs),
		CreatedAt: time.Now(),
	}

	if err := h.auditRepo.Create(auditLog); err != nil {
		h.logger.Error("Failed to create audit log", "error", err, "auditId", auditLog.ID)
	}
}

// handlePing 处理心跳
func (h *WebSocketHandler) handlePing(client *Client, msg *Message) {
	pongMsg, err := NewMessage(
		MessageTypePong,
		msg.RequestID,
		msg.TenantID,
		PongPayload{
			ServerTime: time.Now().UnixMilli(),
		},
	)
	if err != nil {
		h.logger.Error("Failed to create pong message", "error", err)
		return
	}
	if err := client.SendMessage(pongMsg); err != nil {
		h.logger.Error("Failed to send pong message", "error", err)
	}
}
