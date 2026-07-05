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
	var payload websocket.ConnectionPayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse connection payload", "error", err)
		return
	}

	// 设置客户端信息
	client.TenantID = msg.TenantID
	client.PlayerID = payload.PlayerID

	// PlayerID 设置完成后注册到 Hub（Hub 会去重同一 player 的旧连接）
	h.hub.Register <- client

	// 记录 WebSocket 连接指标
	observability.WebSocketConnections.WithLabelValues(msg.TenantID).Inc()

	// 检查客户端是否仍然有效（可能被 Hub 关闭）
	if !h.isClientValid(client) {
		h.logger.Warn("Client was closed by Hub during registration", "playerId", payload.PlayerID)
		observability.WebSocketConnections.WithLabelValues(msg.TenantID).Dec()
		return
	}

	// 查找或创建玩家
	player, err := h.playerRepo.GetByDeviceID(payload.DeviceID, msg.TenantID)
	if err != nil {
		// 创建新玩家
		player = &database.Player{
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
			return
		}
		h.logger.Info("Player created", "playerId", player.ID)
	} else {
		// 更新最后访问时间
		_ = h.playerRepo.UpdateLastVisit(player.ID)
	}

	// 再次检查客户端是否仍然有效
	if !h.isClientValid(client) {
		h.logger.Warn("Client was closed during player lookup", "playerId", payload.PlayerID)
		return
	}

	// 获取会话
	session := h.runtime.GetSession(player.ID, msg.TenantID)
	session.Nickname = payload.Nickname

	// 处理欢迎
	reply, err := h.runtime.HandleWelcome(context.Background(), session)
	if err != nil {
		h.logger.Error("Failed to handle welcome", "error", err)
		// 即使失败也发送欢迎消息
		reply = "欢迎来到江南水乡！我是导游小荷，很高兴为你服务。"
	}

	// 标记已访问
	h.runtime.MarkVisited(session.ID)

	// 构建欢迎消息
	welcomeMsg, err := websocket.NewMessage(
		websocket.MessageTypeWelcome,
		msg.RequestID,
		msg.TenantID,
		websocket.WelcomePayload{
			GuideName:    agent.GuideName,
			Message:      reply,
			IsFirstVisit: session.IsFirstVisit,
			Tips:         []string{"点击输入框与小荷对话", "可以问我关于游戏的问题"},
			PlayerID:     player.ID, // 返回后端生成的玩家ID
		},
	)
	if err != nil {
		h.logger.Error("Failed to create welcome message", "error", err)
		return
	}

	if err := client.SendMessage(welcomeMsg); err != nil {
		h.logger.Error("Failed to send welcome message", "error", err)
		return
	}
}

// handleChatMessage 处理聊天消息
func (h *WebSocketHandler) handleChatMessage(client *websocket.Client, msg *websocket.Message) {
	var payload websocket.ChatMessagePayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse chat payload", "error", err)
		return
	}

	// 查找玩家
	player, err := h.playerRepo.GetByID(payload.PlayerID)
	if err != nil {
		h.logger.Warn("Player not found by ID, trying to find by deviceId", "player_id", payload.PlayerID)

		// 尝试用 deviceId 查找
		player, err = h.playerRepo.GetByDeviceID(client.ID, msg.TenantID)
		if err != nil {
			// 创建新玩家
			player = &database.Player{
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
				errMsg, _ := websocket.NewMessage(
					websocket.MessageTypeError,
					msg.RequestID,
					msg.TenantID,
					websocket.ErrorPayload{
						Code:    "PLAYER_CREATE_ERROR",
						Message: "无法创建玩家信息，请重试。",
					},
				)
				_ = client.SendMessage(errMsg)
				return
			}

			// 发送欢迎消息给新玩家
			welcomeMsg, _ := websocket.NewMessage(
				websocket.MessageTypeWelcome,
				msg.RequestID,
				msg.TenantID,
				websocket.WelcomePayload{
					GuideName:    agent.GuideName,
					Message:      "欢迎来到江南水乡！我是导游小荷，很高兴为你服务。",
					IsFirstVisit: true,
					Tips:         []string{"点击输入框与小荷对话", "可以问我关于游戏的问题"},
					PlayerID:     player.ID,
				},
			)
			_ = client.SendMessage(welcomeMsg)
		}
	}

	// 获取会话
	session := h.runtime.GetSession(player.ID, msg.TenantID)
	eventChan, statsChan, err := h.runtime.HandleChatStream(context.Background(), session, payload.Message)
	if err != nil {
		h.logger.Error("Failed to handle chat stream", "error", err, "player_id", player.ID)

		// 返回错误消息
		errMsg, _ := websocket.NewMessage(
			websocket.MessageTypeError,
			msg.RequestID,
			msg.TenantID,
			websocket.ErrorPayload{
				Code:    "CHAT_ERROR",
				Message: "抱歉，我现在无法回答你的问题。请稍后再试。",
			},
		)
		_ = client.SendMessage(errMsg)
		return
	}

	var fullReply strings.Builder

	// 流式发送响应（只推送有内容或工具调用的事件）
	for event := range eventChan {
		if event.Type == llm.StreamEventTypeChunk && event.Content != "" {
			fullReply.WriteString(event.Content)
		}

		if event.Content == "" && len(event.ToolCalls) == 0 && event.FinishReason == "" {
			continue
		}

		toolCalls := make([]websocket.ToolCall, 0, len(event.ToolCalls))
		for _, tc := range event.ToolCalls {
			toolCalls = append(toolCalls, websocket.ToolCall{
				ID:       tc.ID,
				ToolName: tc.ToolName,
				Params:   tc.Params,
			})
		}

		eventMsg, _ := websocket.NewMessage(
			websocket.MessageTypeStreamEvent,
			msg.RequestID,
			msg.TenantID,
			websocket.StreamEventPayload{
				Type:         string(event.Type),
				Content:      event.Content,
				ToolCalls:    toolCalls,
				ToolResult:   event.ToolResult,
				ActionType:   event.ActionType,
				Model:        event.Model,
				FinishReason: event.FinishReason,
			},
		)
		_ = client.SendMessage(eventMsg)
	}

	// 等待统计信息
	stats := <-statsChan
	h.logger.Info("Chat stream completed", "reply_length", fullReply.Len(), "model", stats.Model, "tokens", stats.TotalTokens)

	// 增加对话计数
	_ = h.playerRepo.IncrementDialogues(player.ID)

	// 保存对话记录（包含 LLM 统计信息和工具使用记录）
	conv := &database.Conversation{
		ID:          uuid.New().String(),
		PlayerID:    player.ID,
		TenantID:    msg.TenantID,
		SessionID:   session.ID,
		UserMessage: payload.Message,
		AIMessage:   fullReply.String(),
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

	// 记录审计日志
	if h.auditRepo == nil {
		h.logger.Error("auditRepo is nil, cannot create audit log")
	} else {
		auditLog := &database.AuditLog{
			ID:             uuid.New().String(),
			TenantID:       msg.TenantID,
			PlayerID:       player.ID,
			Action:         "chat",
			RequestPayload: database.JSON{Data: map[string]string{"message": payload.Message}},
			ResponsePayload: database.JSON{Data: map[string]interface{}{
				"reply":       fullReply.String(),
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
		h.logger.Info("Preparing to save audit log", "auditId", auditLog.ID, "tenantId", auditLog.TenantID, "playerId", auditLog.PlayerID)
		if err := h.auditRepo.Create(auditLog); err != nil {
			h.logger.Error("Failed to create audit log", "error", err, "auditId", auditLog.ID)
		}
	}

	// 记录发出的消息指标
	observability.WebSocketMessagesTotal.WithLabelValues(string(websocket.MessageTypeStreamEvent), "out").Inc()
}

// handlePing 处理心跳
func (h *WebSocketHandler) handlePing(client *websocket.Client, msg *websocket.Message) {
	pongMsg, _ := websocket.NewMessage(
		websocket.MessageTypePong,
		msg.RequestID,
		msg.TenantID,
		websocket.PongPayload{
			ServerTime: time.Now().UnixMilli(),
		},
	)
	_ = client.SendMessage(pongMsg)
}

// isClientValid 检查客户端是否仍然有效（channel 未关闭）
func (h *WebSocketHandler) isClientValid(client *websocket.Client) bool {
	return client.IsValid()
}
