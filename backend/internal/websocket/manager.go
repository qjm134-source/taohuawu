package websocket

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	// 写等待超时
	writeWait = 10 * time.Second
	// 读取等待超时
	pongWait = 35 * time.Second
	// 心跳间隔
	pingPeriod = 30 * time.Second
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
}

// Hub 连接中心
type Hub struct {
	clients    map[string]*Client
	broadcast  chan []byte
	Register   chan *Client
	Unregister chan *Client
	mu         sync.RWMutex
}

// NewHub 创建 Hub
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		broadcast:  make(chan []byte),
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
	}
}

// Run 运行 Hub
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.clients[client.ID] = client
			h.mu.Unlock()

		case client := <-h.Unregister:
			h.mu.Lock()
			if _, ok := h.clients[client.ID]; ok {
				delete(h.clients, client.ID)
				close(client.Send)
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.RLock()
			for _, client := range h.clients {
				select {
				case client.Send <- message:
				default:
					// 客户端缓冲区满，断开连接
					h.Unregister <- client
				}
			}
			h.mu.RUnlock()
		}
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
				go h.UnregisterClient(client)
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

	close(c.Send)
	_ = c.Connection.Close()
}
