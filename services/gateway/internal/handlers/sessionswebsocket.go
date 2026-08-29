package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"

	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
)

// completePersistTimeout bounds how long persisting a session's final
// result may take. Deliberately not tied to the HTTP request's own
// context: by the time ReadPump returns, the WebSocket's underlying
// connection (and the request it was upgraded from) may already be
// closed, which would cancel that context before this write ever runs.
const completePersistTimeout = 5 * time.Second

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
func NewSessionWebSocket(store SessionStore, repo sessionrepo.Repository, analysisRepo analysisrepo.Repository, logger *slog.Logger) gin.HandlerFunc {
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

		final := session.FinalResult()
		completedAt := time.Now()

		persistCtx, cancel := context.WithTimeout(context.Background(), completePersistTimeout)
		defer cancel()

		if err := repo.Complete(persistCtx, sessionID, sessionrepo.Result{
			RiskScore:   final.Assessment.RiskScore,
			Verdict:     string(final.Assessment.Verdict),
			CompletedAt: completedAt,
		}); err != nil {
			logger.Warn("failed to persist final session result",
				slog.String("session_id", sessionID),
				slog.String("error", err.Error()),
			)
		}

		if err := analysisRepo.Create(persistCtx, analysisrepo.Result{
			SessionID:      sessionID,
			VideoFakeScore: final.Assessment.Signals.Video,
			VideoVerdict:   final.VideoVerdict,
			AudioFakeScore: final.Assessment.Signals.Audio,
			AudioVerdict:   final.AudioVerdict,
			TemporalScore:  final.Assessment.Signals.Temporal,
			RiskScore:      final.Assessment.RiskScore,
			RiskVerdict:    string(final.Assessment.Verdict),
			RiskReasons:    final.Assessment.Reasons,
			CreatedAt:      completedAt,
		}); err != nil {
			logger.Warn("failed to persist analysis result",
				slog.String("session_id", sessionID),
				slog.String("error", err.Error()),
			)
		}

		store.Delete(sessionID)
	}
}
