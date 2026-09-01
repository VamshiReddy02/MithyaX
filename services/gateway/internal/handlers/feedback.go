package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// SessionFeedbackRequest is the JSON body accepted by the session
// feedback endpoint.
type SessionFeedbackRequest struct {
	Useful bool `json:"useful"`
}

// NewSessionFeedback builds the POST /api/v1/sessions/:id/feedback
// handler (Phase 8.11): a single "was this detection useful" signal
// from the extension's badge, recorded as one structured log line
// rather than a new table/repository. The first pilot is 5 testers
// sharing one gateway, so `docker compose logs gateway | grep
// "detection feedback"` already gives a complete, centralized view —
// building real storage for this is worth doing only if a larger pilot
// later makes that grep genuinely unwieldy.
//
// Not cross-checked against sessionRepo to confirm :id is a session
// that actually existed — sessionAuth upstream already required a
// valid session credential to reach this handler at all, and at this
// pilot's scale a forged session id on a thumbs-up counter isn't worth
// the extra repository dependency.
func NewSessionFeedback(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req SessionFeedbackRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		logger.Info("detection feedback",
			slog.String("session_id", c.Param("id")),
			slog.Bool("useful", req.Useful),
		)

		c.JSON(http.StatusOK, gin.H{"status": "recorded"})
	}
}
