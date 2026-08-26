package websocket

import (
	"crypto/rand"
	"fmt"
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
	id   string
	hub  *Hub
	conn *gorilla.Conn
	room string
	send chan Message
	log  *slog.Logger
}

var _ peer = (*Client)(nil)

// NewClient wraps an upgraded WebSocket connection for room, assigning it
// a fresh peer ID.
func NewClient(hub *Hub, conn *gorilla.Conn, room string, log *slog.Logger) (*Client, error) {
	id, err := newPeerID()
	if err != nil {
		return nil, err
	}
	return &Client{
		id:   id,
		hub:  hub,
		conn: conn,
		room: room,
		send: make(chan Message, sendBuffer),
		log:  log,
	}, nil
}

// ID returns this client's peer ID, as sent to other peers in From/To
// fields.
func (c *Client) ID() string {
	return c.id
}

func newPeerID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
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

// ReadPump reads signaling messages from the browser and routes them to
// their addressed peer in the room. It blocks until the connection
// closes, then removes the client from its room.
func (c *Client) ReadPump() {
	defer func() {
		c.hub.Leave(c.room, c)
		// Closing send lets WritePump write a graceful close frame and
		// close the connection itself; closing conn here too would race
		// it and could sever the connection before that frame goes out.
		close(c.send)
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
		if msg.To == "" {
			c.log.Warn("dropping message with no target peer", slog.String("type", string(msg.Type)))
			continue
		}

		msg.From = c.id
		if err := c.hub.Route(c.room, msg); err != nil {
			// Benign: the target likely just left mid-negotiation.
			c.log.Warn("failed to route message", slog.String("to", msg.To), slog.String("error", err.Error()))
		}
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
