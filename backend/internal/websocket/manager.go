package websocket

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/pkg/logging"
	"github.com/watertown/guide/pkg/utils"
)

const (
	// 写等待超时
	writeWait = 10 * time.Second
	// 读取等待超时 - 增加到60秒，避免心跳超时
	pongWait = 60 * time.Second
	// 心跳间隔 - 减少到20秒，确保在超时前收到PONG
	pingPeriod = 20 * time.Second
	// 最大消息大小
	maxMessageSize = 1024 * 1024 // 1MB
)

var Upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // 生产环境应该检查 Origin
	},
}

// Client WebSocket 客户端
type Client struct {
	ID         string
	Connection *websocket.Conn
	TenantID   string
	PlayerID   string
	Send       chan []byte
	Pool       *WorkerPool
	mu         sync.Mutex
	closed     bool // 标记连接是否已关闭
}

// Hub 连接中心
type Hub struct {
	clients       map[string]*Client
	playerClients map[string]string // playerID → clientID，用于去重
	broadcast     chan []byte
	Register      chan *Client
	Unregister    chan *Client
	mu            sync.RWMutex
	logger        logging.Logger

	// stop 用于通知 Run 退出。
	stop chan struct{}
	// wg 等待 Run 退出。
	wg sync.WaitGroup
}

// NewHub 创建 Hub。
func NewHub(logger logging.Logger) *Hub {
	return &Hub{
		clients:       make(map[string]*Client),
		playerClients: make(map[string]string),
		broadcast:     make(chan []byte),
		Register:      make(chan *Client),
		Unregister:    make(chan *Client),
		logger:        logger,
		stop:          make(chan struct{}),
	}
}

// Run 运行 Hub，直到 Stop() 被调用。
func (h *Hub) Run() {
	h.wg.Add(1)
	defer h.wg.Done()

	for {
		select {
		case client := <-h.Register:
			h.handleRegister(client)

		case client := <-h.Unregister:
			h.handleUnregister(client)

		case message := <-h.broadcast:
			h.handleBroadcast(message)

		case <-h.stop:
			return
		}
	}
}

// Stop 通知 Hub 退出并等待 Run 返回。
func (h *Hub) Stop() {
	close(h.stop)
	h.wg.Wait()
}

func (h *Hub) handleRegister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 同一 player 去重：如果有旧连接，关闭旧连接
	if client.PlayerID != "" {
		if oldClientID, exists := h.playerClients[client.PlayerID]; exists {
			if oldClient, ok := h.clients[oldClientID]; ok {
				// 直接关闭旧连接，不通过 channel 避免死锁
				h.removeClientLocked(oldClient)
			}
		}
		h.playerClients[client.PlayerID] = client.ID
	}
	h.clients[client.ID] = client
}

func (h *Hub) handleUnregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeClientLocked(client)
}

func (h *Hub) handleBroadcast(message []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, client := range h.clients {
		select {
		case client.Send <- message:
		default:
			// 客户端缓冲区满，断开连接
			go func(c *Client) {
				defer utils.RecoverWithCustomLogger("Hub.UnregisterClient", h.logger)
				h.UnregisterClient(c)
			}(client)
		}
	}
}

// removeClientLocked 在持有锁的情况下移除客户端（内部方法）
func (h *Hub) removeClientLocked(client *Client) {
	if _, ok := h.clients[client.ID]; ok {
		delete(h.clients, client.ID)
		if client.PlayerID != "" {
			// 只有当前映射指向这个 client 时才删除
			if h.playerClients[client.PlayerID] == client.ID {
				delete(h.playerClients, client.PlayerID)
			}
		}
		// 安全关闭 Send channel
		client.mu.Lock()
		if !client.closed {
			client.closed = true
			close(client.Send)
		}
		client.mu.Unlock()
	}
}

// BroadcastToTenant 广播消息到指定租户
func (h *Hub) BroadcastToTenant(tenantID string, message []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, client := range h.clients {
		if client.TenantID == tenantID {
			select {
			case client.Send <- message:
			default:
				// 客户端缓冲区满，断开连接
				go func(c *Client) {
					defer utils.RecoverWithCustomLogger("Hub.UnregisterClient", h.logger)
					h.UnregisterClient(c)
				}(client)
			}
		}
	}
}

// UnregisterClient 注销客户端
func (h *Hub) UnregisterClient(client *Client) {
	h.Unregister <- client
}

// WritePump 写入泵
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.Connection.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Connection.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.Connection.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Connection.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.Connection.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ReadPump 读取泵
func (c *Client) ReadPump(hub *Hub, handler MessageHandler) {
	defer func() {
		hub.UnregisterClient(c)
		if c.TenantID != "" {
			observability.WebSocketConnections.WithLabelValues(c.TenantID).Dec()
		}
		_ = c.Connection.Close()
	}()

	c.Connection.SetReadLimit(maxMessageSize)
	c.Connection.SetReadDeadline(time.Now().Add(pongWait))
	c.Connection.SetPongHandler(func(string) error {
		c.Connection.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.Connection.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				// 记录错误
			}
			break
		}

		// 处理消息
		if handler != nil {
			handler(c, message)
		}
	}
}

// MessageHandler 消息处理器
type MessageHandler func(client *Client, message []byte)

// NewClient 创建客户端
func NewClient(conn *websocket.Conn, tenantID, playerID string, pool *WorkerPool) *Client {
	return &Client{
		ID:         uuid.New().String(),
		Connection: conn,
		TenantID:   tenantID,
		PlayerID:   playerID,
		Send:       make(chan []byte, 256),
		Pool:       pool,
	}
}

// SendMessage 发送消息
func (c *Client) SendMessage(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return errors.New("client is closed")
	}

	select {
	case c.Send <- data:
		return nil
	default:
		return errors.New("client send channel is full")
	}
}

// Close 关闭连接
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return // 避免重复关闭
	}
	c.closed = true
	close(c.Send)
	_ = c.Connection.Close()
}

// IsClosed 检查连接是否已关闭
func (c *Client) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// IsValid 检查客户端是否仍然有效（channel 未关闭）
func (c *Client) IsValid() bool {
	return !c.IsClosed()
}
