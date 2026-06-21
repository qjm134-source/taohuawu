package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/watertown/guide/internal/agent"
	"github.com/watertown/guide/internal/database"
	"github.com/watertown/guide/internal/websocket"
	"github.com/watertown/guide/pkg/logging"
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

	// 创建客户端
	client := websocket.NewClient(conn, "", "", nil)

	// 注册客户端到 Hub
	h.hub.Register <- client

	// 启动读写泵
	go client.WritePump()
	go client.ReadPump(h.hub, h.handleMessage)
}

// handleMessage 处理消息
func (h *WebSocketHandler) handleMessage(client *websocket.Client, message []byte) {
	h.logger.Info("Received raw message", "length", len(message), "preview", string(message[:min(len(message), 200)]))

	// 解析消息
	var msg websocket.Message
	if err := json.Unmarshal(message, &msg); err != nil {
		h.logger.Error("Failed to parse message", "error", err)
		return
	}

	h.logger.Info("Parsed message", "type", msg.Type, "requestId", msg.RequestID, "tenantId", msg.TenantID)

	switch msg.Type {
	case websocket.MessageTypeConnection:
		h.logger.Info("Routing message to connection handler", "type", msg.Type)
		h.handleConnection(client, &msg)
	case websocket.MessageTypeChatMessage:
		h.logger.Info("Routing message to chat handler", "type", msg.Type)
		h.handleChatMessage(client, &msg)
	case websocket.MessageTypePing:
		h.logger.Info("Routing message to ping handler", "type", msg.Type)
		h.handlePing(client, &msg)
	default:
		h.logger.Warn("Unknown message type", "type", msg.Type)
	}
}

// handleConnection 处理连接消息
func (h *WebSocketHandler) handleConnection(client *websocket.Client, msg *websocket.Message) {
	h.logger.Info("Handling connection", "requestId", msg.RequestID, "tenantId", msg.TenantID)

	var payload websocket.ConnectionPayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse connection payload", "error", err)
		return
	}

	h.logger.Info("Connection payload parsed", "playerId", payload.PlayerID, "deviceId", payload.DeviceID, "nickname", payload.Nickname)

	// 设置客户端信息
	client.TenantID = msg.TenantID
	client.PlayerID = payload.PlayerID

	// 查找或创建玩家
	player, err := h.playerRepo.GetByDeviceID(payload.DeviceID, msg.TenantID)
	if err != nil {
		h.logger.Info("Player not found, creating new player", "deviceId", payload.DeviceID)
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
		h.logger.Info("Player found", "playerId", player.ID, "nickname", player.Nickname)
		// 更新最后访问时间
		_ = h.playerRepo.UpdateLastVisit(player.ID)
	}

	// 获取会话
	session := h.runtime.GetSession(player.ID, msg.TenantID)
	session.Nickname = payload.Nickname

	h.logger.Info("Getting welcome message", "playerId", player.ID)

	// 处理欢迎
	reply, err := h.runtime.HandleWelcome(context.Background(), session)
	if err != nil {
		h.logger.Error("Failed to handle welcome", "error", err)
		// 即使失败也发送欢迎消息
		reply = "欢迎来到江南水乡！我是导游小荷，很高兴为你服务。"
	}

	h.logger.Info("Welcome message generated", "reply_length", len(reply))

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

	h.logger.Info("Sending welcome message", "playerId", player.ID, "isFirstVisit", session.IsFirstVisit)

	if err := client.SendMessage(welcomeMsg); err != nil {
		h.logger.Error("Failed to send welcome message", "error", err)
		return
	}

	h.logger.Info("Welcome message sent successfully")
}

// handleChatMessage 处理聊天消息
func (h *WebSocketHandler) handleChatMessage(client *websocket.Client, msg *websocket.Message) {
	h.logger.Info("Handling chat message", "requestId", msg.RequestID)

	var payload websocket.ChatMessagePayload
	if err := msg.ParsePayload(&payload); err != nil {
		h.logger.Error("Failed to parse chat payload", "error", err)
		return
	}

	h.logger.Info("Chat payload parsed", "playerId", payload.PlayerID, "message", payload.Message)

	// 查找玩家
	player, err := h.playerRepo.GetByID(payload.PlayerID)
	if err != nil {
		h.logger.Warn("Player not found by ID, trying to find by deviceId", "player_id", payload.PlayerID)

		// 尝试用 deviceId 查找
		player, err = h.playerRepo.GetByDeviceID(client.ID, msg.TenantID)
		if err != nil {
			h.logger.Warn("Player not found by deviceId, creating new player", "deviceId", client.ID)

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

			h.logger.Info("New player created", "playerId", player.ID)

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

	h.logger.Info("Player found", "player_id", player.ID, "nickname", player.Nickname)

	// 获取会话
	session := h.runtime.GetSession(player.ID, msg.TenantID)
	h.logger.Info("Session retrieved", "sessionId", session.ID)

	// 处理聊天
	h.logger.Info("Calling LLM for chat", "player_id", player.ID, "message", payload.Message)
	reply, emotion, stats, err := h.runtime.HandleChat(context.Background(), session, payload.Message)
	if err != nil {
		h.logger.Error("Failed to handle chat", "error", err, "player_id", player.ID)

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

	h.logger.Info("Chat response received", "reply_length", len(reply), "emotion", emotion, "model", stats.Model, "tokens", stats.TotalTokens)

	// 增加对话计数
	_ = h.playerRepo.IncrementDialogues(player.ID)

	// 保存对话记录（包含 LLM 统计信息和工具使用记录）
	conv := &database.Conversation{
		ID:          uuid.New().String(),
		PlayerID:    player.ID,
		TenantID:    msg.TenantID,
		SessionID:   session.ID,
		UserMessage: payload.Message,
		AIMessage:   reply,
		Emotion:     emotion,
		ToolsUsed:   database.JSON{Data: stats.ToolsUsed},
		LLMModel:    stats.Model,
		LLMTokens:   stats.TotalTokens,
		Cost:        stats.Cost,
		CacheHit:    stats.CacheHit,
		CreatedAt:   time.Now(),
	}
	if err := h.convRepo.Create(conv); err != nil {
		h.logger.Error("Failed to create conversation", "error", err)
	} else {
		h.logger.Info("Conversation saved", "convId", conv.ID)
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
		h.logger.Info("Preparing to save audit log", "auditId", auditLog.ID, "tenantId", auditLog.TenantID, "playerId", auditLog.PlayerID)
		if err := h.auditRepo.Create(auditLog); err != nil {
			h.logger.Error("Failed to create audit log", "error", err, "auditId", auditLog.ID)
		} else {
			h.logger.Info("Audit log saved successfully", "auditId", auditLog.ID, "tenantId", auditLog.TenantID)
		}
	}

	// 构建回复消息（包含 LLM 统计信息）
	replyMsg, _ := websocket.NewMessage(
		websocket.MessageTypeNPCReply,
		msg.RequestID,
		msg.TenantID,
		websocket.NPCReplyPayload{
			GuideName: agent.GuideName,
			Message:   reply,
			Emotion:   emotion,
			Actions:   []string{},
			Stats: &websocket.LLMStats{
				Model:        stats.Model,
				LatencyMs:    stats.LatencyMs,
				InputTokens:  stats.InputTokens,
				OutputTokens: stats.OutputTokens,
				TotalTokens:  stats.TotalTokens,
				Cost:         stats.Cost,
				CacheHit:     stats.CacheHit,
			},
		},
	)

	h.logger.Info("Sending NPC reply", "message_length", len(reply))
	_ = client.SendMessage(replyMsg)
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
