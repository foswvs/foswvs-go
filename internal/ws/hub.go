package ws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// MessageType identifies the kind of WebSocket message.
type MessageType string

const (
	// Client-facing messages
	MsgDataUsage     MessageType = "data_usage"
	MsgNetworkStatus MessageType = "network_status"
	MsgTopupProgress MessageType = "topup_progress"
	MsgTopupDone     MessageType = "topup_done"
	MsgShareReceived MessageType = "share_received"
	MsgAlert         MessageType = "alert"
	MsgUnregistered  MessageType = "unregistered"
	MsgDeviceToken   MessageType = "device_token"
	MsgMaintenance   MessageType = "maintenance"

	// Admin-facing messages
	MsgActiveDevices MessageType = "active_devices"
	MsgEarnings      MessageType = "earnings"
	MsgBandwidth     MessageType = "bandwidth"
	MsgDeviceUpdate  MessageType = "device_update"
	MsgSystemInfo    MessageType = "system_info"
)

// Envelope is the standard WebSocket message wrapper.
type Envelope struct {
	Type MessageType `json:"type"`
	Data interface{} `json:"data"`
}

// Client represents a connected WebSocket client.
type Client struct {
	hub     *Hub
	conn    *websocket.Conn
	send    chan []byte
	ip      string
	isAdmin bool
	id      string // unique client ID
}

// Hub manages all WebSocket connections and message routing.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub event loop.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for c := range h.clients {
				close(c.send)
				c.conn.Close()
			}
			h.mu.Unlock()
			return

		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("ws: client connected ip=%s admin=%v (total=%d)", client.ip, client.isAdmin, len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			log.Printf("ws: client disconnected ip=%s (total=%d)", client.ip, len(h.clients))

		case msg := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// buffer full, drop
				}
			}
			h.mu.RUnlock()
		}
	}
}

// SendToIP sends a message to all clients with the given IP.
func (h *Hub) SendToIP(ip string, msgType MessageType, data interface{}) {
	env := Envelope{Type: msgType, Data: data}
	raw, err := json.Marshal(env)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.ip == ip && !c.isAdmin {
			select {
			case c.send <- raw:
			default:
			}
		}
	}
}

// SendToClient sends a message to one specific WebSocket client.
func (h *Hub) SendToClient(client *Client, msgType MessageType, data interface{}) {
	if client == nil {
		return
	}
	env := Envelope{Type: msgType, Data: data}
	raw, err := json.Marshal(env)
	if err != nil {
		return
	}
	select {
	case client.send <- raw:
	default:
	}
}

// SendToAdmins sends a message to all admin WebSocket clients.
func (h *Hub) SendToAdmins(msgType MessageType, data interface{}) {
	env := Envelope{Type: msgType, Data: data}
	raw, err := json.Marshal(env)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.isAdmin {
			select {
			case c.send <- raw:
			default:
			}
		}
	}
}

// BroadcastAll sends a message to every connected client.
func (h *Hub) BroadcastAll(msgType MessageType, data interface{}) {
	env := Envelope{Type: msgType, Data: data}
	raw, err := json.Marshal(env)
	if err != nil {
		return
	}
	h.broadcast <- raw
}

// NewClientWithIP creates a client WS handler that stores the real client
// IP. It also returns the device token, if any, that the browser carried in
// the Sec-WebSocket-Protocol handshake header — browsers don't allow custom
// headers on WebSocket connections, so a subprotocol value is the standard
// way to smuggle an auth token through the handshake. Per RFC 6455 the
// server must echo back whatever subprotocol it accepts, which is done here
// via the upgrade response header.
func (h *Hub) NewClientWithIP(w http.ResponseWriter, r *http.Request, clientIP string) (*Client, string) {
	var token string
	var responseHeader http.Header
	if protocols := websocket.Subprotocols(r); len(protocols) > 0 {
		token = protocols[0]
		responseHeader = http.Header{"Sec-WebSocket-Protocol": {token}}
	}

	conn, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return nil, ""
	}

	client := &Client{
		hub:     h,
		conn:    conn,
		send:    make(chan []byte, 64),
		ip:      clientIP,
		isAdmin: false,
	}

	h.register <- client
	go client.writePump()
	go client.readPump()
	return client, token
}

// NewAdminClient upgrades and registers an admin WS client, returning it so
// the caller can push initial state (mirrors NewClientWithIP).
func (h *Hub) NewAdminClient(w http.ResponseWriter, r *http.Request) *Client {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws admin upgrade: %v", err)
		return nil
	}

	client := &Client{
		hub:     h,
		conn:    conn,
		send:    make(chan []byte, 64),
		ip:      r.RemoteAddr,
		isAdmin: true,
	}

	h.register <- client
	go client.writePump()
	go client.readPump()
	return client
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(4096)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		// Client messages can be handled here in the future
		// (e.g., client requesting specific data, ping, etc.)
	}
}

// HasClientsForIP checks if any client WS connections exist for an IP.
func (h *Hub) HasClientsForIP(ip string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.ip == ip && !c.isAdmin {
			return true
		}
	}
	return false
}

// HasAdminClients checks if any admin WS connections exist.
func (h *Hub) HasAdminClients() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.isAdmin {
			return true
		}
	}
	return false
}
