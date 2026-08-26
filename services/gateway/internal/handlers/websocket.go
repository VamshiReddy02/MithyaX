package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"

	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
)

var upgrader = gorilla.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Signaling is opened directly from browser JS on another origin
	// (the web frontend). Revisit once that origin is known.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// NewWebSocket builds the /api/v1/ws signaling handler backed by hub. It
// upgrades the HTTP connection, joins the caller into the room named by
// the "room" query parameter, tells it who's already there, and routes
// its signaling messages to their addressed peers.
func NewWebSocket(hub *websocket.Hub, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		room := c.Query("room")
		if room == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "room is required"})
			return
		}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			logger.Warn("websocket upgrade failed", slog.String("error", err.Error()))
			return
		}

		client, err := websocket.NewClient(hub, conn, room, logger)
		if err != nil {
			logger.Error("failed to create websocket client", slog.String("error", err.Error()))
			conn.Close()
			return
		}

		existingPeers, err := hub.Join(room, client)
		if err != nil {
			client.Close(gorilla.ClosePolicyViolation, err.Error())
			return
		}

		go client.WritePump()
		client.Send(websocket.Message{Type: websocket.TypePeers, Peers: existingPeers})
		client.ReadPump()
	}
}
