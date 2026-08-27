package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
)

// SessionCreator creates a new live analysis session. *realtime.Store
// implements it.
type SessionCreator interface {
	Create() (*realtime.Session, error)
}

// NewCreateSession builds the POST /api/v1/sessions handler: it creates
// a new live analysis session and returns its ID, ready for the browser
// to open a WebSocket to via /api/v1/sessions/ws?session_id=....
func NewCreateSession(store SessionCreator) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := store.Create()
		if err != nil {
			if errors.Is(err, realtime.ErrTooManySessions) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "too many active sessions"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"id":     session.ID(),
			"status": string(session.Status()),
		})
	}
}

// SessionMetricsProvider reports a point-in-time snapshot of the live
// session pipeline's counters and gauges. *realtime.Store implements it.
type SessionMetricsProvider interface {
	Metrics() realtime.MetricsSnapshot
}

// NewSessionMetrics builds the GET /api/v1/sessions/metrics handler: a
// lightweight, no-dependency way to see whether the realtime pipeline
// is keeping up (queue depths, drop counts, inference latency) —
// groundwork for Phase 10's observability work, not a replacement for
// it.
func NewSessionMetrics(store SessionMetricsProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, store.Metrics())
	}
}
