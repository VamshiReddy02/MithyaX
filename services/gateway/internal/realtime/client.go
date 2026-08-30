package realtime

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	gorilla "github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	// maxMessageSize needs real headroom over websocket.Hub's signaling
	// limit: a message here carries a base64-encoded JPEG frame or audio
	// chunk, not a short SDP/ICE payload.
	maxMessageSize = 32 << 20 // 32MiB
	sendBuffer     = 16
)

// Client wires one browser's WebSocket connection to its Session:
// InMessages read off the wire are decoded and handed to the Session's
// bounded queues, and every OutMessage the Session's workers produce is
// forwarded to this client's outbound queue. It owns no analysis logic
// itself — see Session for that; Client is transport only, the same
// split websocket.Client/Hub already use for signaling.
//
// Reading is still one message at a time (gorilla's ReadJSON blocks
// until the next frame arrives), but unlike the pipeline's first cut,
// handling a frame/audio_chunk message no longer blocks on the actual
// detector call — it just enqueues the work and returns immediately, so
// the connection can keep reading (and queuing, and dropping stale
// video frames) even while every worker is busy.
type Client struct {
	session *Session
	conn    *gorilla.Conn
	send    chan OutMessage
	log     *slog.Logger

	// forwardDone is closed once the goroutine forwarding
	// session.Out() into send has exited. ReadPump's cleanup waits on
	// it before closing send, since that forwarder is the only other
	// goroutine that ever sends on it — closing send first would risk a
	// send-on-closed-channel panic if the forwarder was mid-send.
	forwardDone chan struct{}
}

// NewClient wraps an upgraded WebSocket connection for session.
func NewClient(session *Session, conn *gorilla.Conn, log *slog.Logger) *Client {
	return &Client{
		session:     session,
		conn:        conn,
		send:        make(chan OutMessage, sendBuffer),
		log:         log,
		forwardDone: make(chan struct{}),
	}
}

// Send queues msg for delivery to this client. It never blocks; if the
// client's outbound buffer is full, the message is dropped rather than
// stalling a worker goroutine or the read loop.
func (c *Client) Send(msg OutMessage) {
	select {
	case c.send <- msg:
	default:
		c.log.Warn("dropping message: client send buffer full", slog.String("session_id", c.session.ID()))
	}
}

// ForwardSessionMessages relays every message the session's workers
// produce (see Session.Out) to this client's outbound queue. Run it in
// its own goroutine, alongside WritePump — it exits once the session
// closes Out(), which happens once every worker has stopped (see
// Session.End).
func (c *Client) ForwardSessionMessages() {
	defer close(c.forwardDone)
	for msg := range c.session.Out() {
		c.Send(msg)
	}
}

// ReadPump reads frame/audio_chunk/end_session messages from the
// browser and hands them to the Session's queues. It blocks until the
// connection closes (or end_session is received), then ends the
// session, waits for ForwardSessionMessages to finish draining it, and
// only then closes send — the point at which WritePump knows no more
// messages are coming and can write a close frame.
func (c *Client) ReadPump() {
	defer func() {
		c.session.End()
		<-c.forwardDone
		close(c.send)
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.resetReadDeadline()
	c.conn.SetPongHandler(func(string) error {
		c.resetReadDeadline()
		return nil
	})

	for {
		var in InMessage
		if err := c.conn.ReadJSON(&in); err != nil {
			return
		}

		// 7.7.6: a session that has processed more than its configured
		// maximum frames/audio chunks is done for good, regardless of
		// which message type arrives next — checked once per message
		// here rather than duplicated in handleFrame/handleAudioChunk.
		if reason, exceeded := c.session.countLimitExceeded(); exceeded {
			c.Send(OutMessage{Type: TypeError, Code: ErrCodeSessionLimitExceeded, Message: reason})
			c.Send(c.session.End())
			return
		}

		switch in.Type {
		case TypeFrame:
			c.handleFrame(in)
		case TypeAudioChunk:
			c.handleAudioChunk(in)
		case TypeEndSession:
			c.Send(c.session.End())
			return
		default:
			c.Send(OutMessage{Type: TypeError, Message: fmt.Sprintf("unknown message type %q", in.Type)})
		}
	}
}

// resetReadDeadline pushes the connection's read deadline out by
// pongWait, the same keep-alive mechanism every WebSocket in this
// package uses — except clamped to never exceed the session's own
// Deadline (7.7.6's maximum session duration), if one is configured.
// Once that clamp takes effect, the next read (including just the
// keep-alive pong itself) times out right at the boundary, and
// ReadPump's normal error-triggers-cleanup path takes it from there —
// no separate timer needed, and it works the same whether the session
// is actively streaming or has simply gone idle.
func (c *Client) resetReadDeadline() {
	deadline := time.Now().Add(pongWait)
	if sessionDeadline := c.session.Deadline(); !sessionDeadline.IsZero() && sessionDeadline.Before(deadline) {
		deadline = sessionDeadline
	}
	c.conn.SetReadDeadline(deadline)
}

func (c *Client) handleFrame(in InMessage) {
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		c.Send(OutMessage{Type: TypeError, Message: "frame data is not valid base64"})
		return
	}

	if !c.session.SubmitFrame(data) {
		c.Send(OutMessage{Type: TypeError, Code: ErrCodeOverloaded, Message: "video queue is full"})
	}
}

func (c *Client) handleAudioChunk(in InMessage) {
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		c.Send(OutMessage{Type: TypeError, Message: "audio_chunk data is not valid base64"})
		return
	}

	filename := in.Filename
	if filename == "" {
		filename = "chunk.wav"
	}

	if !c.session.SubmitAudioChunk(filename, data) {
		c.Send(OutMessage{Type: TypeError, Code: ErrCodeOverloaded, Message: "audio queue is full"})
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
