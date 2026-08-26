package websocket

import (
	"log/slog"
	"time"

	gorilla "github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 32 * 1024
	sendBuffer     = 16
)

// Client represents one browser connected to the signaling endpoint.
type Client struct {
	hub  *Hub
	conn *gorilla.Conn
	room string
	send chan Message
	log  *slog.Logger
}

var _ peer = (*Client)(nil)

// NewClient wraps an upgraded WebSocket connection for room.
func NewClient(hub *Hub, conn *gorilla.Conn, room string, log *slog.Logger) *Client {
	return &Client{
		hub:  hub,
		conn: conn,
		room: room,
		send: make(chan Message, sendBuffer),
		log:  log,
	}
}

// Send queues msg for delivery to this client. It never blocks; if the
// client's outbound buffer is full, the message is dropped rather than
// stalling the hub.
func (c *Client) Send(msg Message) {
	select {
	case c.send <- msg:
	default:
		c.log.Warn("dropping message: client send buffer full", slog.String("room", c.room))
	}
}

// ReadPump reads signaling messages from the browser and relays them to
// the other peer in the room. It blocks until the connection closes, then
// removes the client from its room.
func (c *Client) ReadPump() {
	defer func() {
		c.hub.Leave(c.room, c)
		close(c.send)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		var msg Message
		if err := c.conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg.Type == TypeLeave {
			return
		}
		c.hub.Broadcast(c.room, c, msg)
	}
}

// WritePump writes queued messages and periodic pings to the browser. It
// blocks until the connection closes or its send channel is closed.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(gorilla.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteJSON(msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(gorilla.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Close sends a close frame with the given code and reason, then closes
// the underlying connection. Used to reject a client before its pumps
// start (e.g. the room is full).
func (c *Client) Close(code int, reason string) {
	deadline := time.Now().Add(writeWait)
	c.conn.WriteControl(gorilla.CloseMessage, gorilla.FormatCloseMessage(code, reason), deadline)
	c.conn.Close()
}
