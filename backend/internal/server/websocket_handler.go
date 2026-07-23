package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/watertown/guide/internal/agent"
	"github.com/watertown/guide/internal/database"
	"github.com/watertown/guide/internal/llm"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/internal/websocket"
	"github.com/watertown/guide/pkg/logging"
	"github.com/watertown/guide/pkg/utils"
)

// WebSocketHandler WebSocket 处理器
type WebSocketHandler struct {
	hub            *websocket.Hub
	sessionManager *agent.SessionManager
	runtime        *agent.Runtime
	playerRepo     database.PlayerRepository
	convRepo       database.ConversationRepository
	auditRepo      database.AuditRepository
	logger         logging.Logger
}

// NewWebSocketHandler 创建 WebSocket 处理器
func NewWebSocketHandler(
	hub *websocket.Hub,
	sessionManager *agent.SessionManager,
	runtime *agent.Runtime,
	playerRepo database.PlayerRepository,
	convRepo database.ConversationRepository,
	auditRepo database.AuditRepository,
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
	conn, err := websocket.Upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Error("WebSocket upgrade failed", "error", err)
		return
	}

	// 创建客户端（暂不注册，等收到 CONNECTION 消息后再注册以完成去重）
	client := websocket.NewClient(conn, "", "", nil)

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
func (h *WebSocketHandler) handleMessage(client *websocket.Client, message []byte) {
	// 解析消息
	var msg websocket.Message
	if err := json.Unmarshal(message, &msg); err != nil {
		h.logger.Error("Failed to parse message", "error", err)
		return
	}

	// 记录接收消息指标
	observability.WebSocketMessagesTotal.WithLabelValues(string(msg.Type), "in").Inc()

	switch msg.Type {
	case websocket.MessageTypeConnection:
		h.handleConnection(client, &msg)
	case websocket.MessageTypeChatMessage:
		h.handleChatMessage(client, &msg)
	case websocket.MessageTypePing:
		h.handlePing(client, &msg)
	default:
		h.logger.Warn("Unknown message type", "type", msg.Type)
	}
}

// handleConnection 处理连接消息
func (h *WebSocketHandler) handleConnection(client *websocket.Client, msg *websocket.Message) {
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

func (h *WebSocketHandler) parseConnectionPayload(client *websocket.Client, msg *websocket.Message) (websocket.ConnectionPayload, bool) {
	var payload websocket.ConnectionPayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse connection payload", "error", err)
		return payload, false
	}
	client.TenantID = msg.TenantID
	client.PlayerID = payload.PlayerID
	return payload, true
}

func (h *WebSocketHandler) registerClient(client *websocket.Client, msg *websocket.Message, payload websocket.ConnectionPayload) {
	h.hub.Register <- client
	observability.WebSocketConnections.WithLabelValues(msg.TenantID).Inc()
}

func (h *WebSocketHandler) ensurePlayer(payload websocket.ConnectionPayload, msg *websocket.Message) (*database.Player, bool, bool) {
	player, err := h.playerRepo.GetByDeviceID(payload.DeviceID, msg.TenantID)
	if err != nil {
		return h.createPlayer(payload, msg)
	}

	if err := h.playerRepo.UpdateLastVisit(player.ID); err != nil {
		h.logger.Error("Failed to update last visit", "error", err, "player_id", player.ID)
	}
	return player, false, true
}

func (h *WebSocketHandler) createPlayer(payload websocket.ConnectionPayload, msg *websocket.Message) (*database.Player, bool, bool) {
	player := &database.Player{
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

func (h *WebSocketHandler) sendWelcome(client *websocket.Client, msg *websocket.Message, player *database.Player, isNewPlayer bool) {
	session := h.runtime.GetSession(player.ID, msg.TenantID)
	session.Nickname = player.Nickname

	reply, err := h.runtime.HandleWelcome(context.Background(), session)
	if err != nil {
		h.logger.Error("Failed to handle welcome", "error", err)
		reply = "欢迎来到江南水乡！我是导游小荷，很高兴为你服务。"
	}

	h.runtime.MarkVisited(session.ID)

	welcomeMsg, err := websocket.NewMessage(
		websocket.MessageTypeWelcome,
		msg.RequestID,
		msg.TenantID,
		websocket.WelcomePayload{
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
func (h *WebSocketHandler) handleChatMessage(client *websocket.Client, msg *websocket.Message) {
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

func (h *WebSocketHandler) parseChatPayload(client *websocket.Client, msg *websocket.Message) (websocket.ChatMessagePayload, bool) {
	var payload websocket.ChatMessagePayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse chat payload", "error", err)
		return payload, false
	}
	return payload, true
}

func (h *WebSocketHandler) ensureChatPlayer(client *websocket.Client, msg *websocket.Message, payload websocket.ChatMessagePayload) (*database.Player, bool) {
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

func (h *WebSocketHandler) createChatPlayer(client *websocket.Client, msg *websocket.Message) (*database.Player, bool) {
	player := &database.Player{
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

func (h *WebSocketHandler) sendError(client *websocket.Client, msg *websocket.Message, code, message string) {
	errMsg, err := websocket.NewMessage(
		websocket.MessageTypeError,
		msg.RequestID,
		msg.TenantID,
		websocket.ErrorPayload{
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

func (h *WebSocketHandler) streamResponse(client *websocket.Client, msg *websocket.Message,
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

func (h *WebSocketHandler) sendStreamEvent(client *websocket.Client, msg *websocket.Message, event *llm.StreamEvent) {
	toolCalls := make([]websocket.ToolCall, 0, len(event.ToolCalls))
	for _, tc := range event.ToolCalls {
		toolCalls = append(toolCalls, websocket.ToolCall{
			ID:       tc.ID,
			ToolName: tc.ToolName,
			Params:   tc.Params,
		})
	}

	eventMsg, err := websocket.NewMessage(
		websocket.MessageTypeStreamEvent,
		msg.RequestID,
		msg.TenantID,
		websocket.StreamEventPayload{
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

func (h *WebSocketHandler) sendCompleteEvent(client *websocket.Client, msg *websocket.Message, stats *agent.LLMStats) {
	completeMsg, err := websocket.NewMessage(
		websocket.MessageTypeStreamEvent,
		msg.RequestID,
		msg.TenantID,
		websocket.StreamEventPayload{
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

func (h *WebSocketHandler) persistChatResult(session *agent.Session, player *database.Player, msg *websocket.Message,
	userMessage, reply string, stats *agent.LLMStats) {

	if err := h.playerRepo.IncrementDialogues(player.ID); err != nil {
		h.logger.Error("Failed to increment dialogues", "error", err, "player_id", player.ID)
	}

	h.saveConversation(session, player, msg, userMessage, reply, stats)
	h.saveAuditLog(session, player, msg, userMessage, reply, stats)

	observability.WebSocketMessagesTotal.WithLabelValues(string(websocket.MessageTypeStreamEvent), "out").Inc()
}

func (h *WebSocketHandler) saveConversation(session *agent.Session, player *database.Player, msg *websocket.Message,
	userMessage, reply string, stats *agent.LLMStats) {

	conv := &database.Conversation{
		ID:          uuid.New().String(),
		PlayerID:    player.ID,
		TenantID:    msg.TenantID,
		SessionID:   session.ID,
		UserMessage: userMessage,
		AIMessage:   reply,
		Emotion:     stats.Model, // 临时使用，实际应该从上下文获取
		ToolsUsed:   database.JSON{Data: stats.ToolsUsed},
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

func (h *WebSocketHandler) saveAuditLog(session *agent.Session, player *database.Player, msg *websocket.Message,
	userMessage, reply string, stats *agent.LLMStats) {

	if h.auditRepo == nil {
		h.logger.Error("auditRepo is nil, cannot create audit log")
		return
	}

	auditLog := &database.AuditLog{
		ID:             uuid.New().String(),
		TenantID:       msg.TenantID,
		PlayerID:       player.ID,
		Action:         "chat",
		RequestPayload: database.JSON{Data: map[string]string{"message": userMessage}},
		ResponsePayload: database.JSON{Data: map[string]interface{}{
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
func (h *WebSocketHandler) handlePing(client *websocket.Client, msg *websocket.Message) {
	pongMsg, err := websocket.NewMessage(
		websocket.MessageTypePong,
		msg.RequestID,
		msg.TenantID,
		websocket.PongPayload{
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
