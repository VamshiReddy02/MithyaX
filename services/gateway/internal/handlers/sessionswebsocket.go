package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"

	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
)

// SessionStore looks up and removes live analysis sessions.
// *realtime.Store implements it.
type SessionStore interface {
	Get(id string) (*realtime.Session, bool)
	Delete(id string)
}

var sessionUpgrader = gorilla.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Same as the signaling upgrader: opened directly from browser JS on
	// another origin. Revisit once that origin is known.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// NewSessionWebSocket builds the /api/v1/sessions/ws handler: it looks
// up the session named by the "session_id" query parameter (created via
// POST /api/v1/sessions), upgrades the connection, and streams frame/
// audio_chunk messages in and video_result/audio_result/temporal_result/
// risk_update messages back out for as long as it stays open.
//
// This is deliberately a separate endpoint from /api/v1/ws — that one
// is WebRTC signaling between browsers in a room (see internal/
// websocket); this one is a single browser streaming its own analysis
// session. They solve unrelated problems and shouldn't share a route.
func NewSessionWebSocket(store SessionStore, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID := c.Query("session_id")
		if sessionID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
			return
		}

		session, ok := store.Get(sessionID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}

		conn, err := sessionUpgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			logger.Warn("session websocket upgrade failed", slog.String("error", err.Error()))
			return
		}

		client := realtime.NewClient(session, conn, logger)

		go client.WritePump()
		go client.ForwardSessionMessages()
		client.Send(realtime.OutMessage{
			Type:   realtime.TypeSessionStarted,
			ID:     session.ID(),
			Status: string(session.Status()),
		})
		client.ReadPump()

		store.Delete(sessionID)
	}
}
