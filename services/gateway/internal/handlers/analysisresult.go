package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
)

// NewGetAnalysisResult builds the GET /api/v1/sessions/:id/analysis
// handler: the answer to "why did MithyaX classify this session the way
// it did" — the persisted per-modality breakdown behind a completed
// session's risk score, looked up long after the live WebSocket session
// (and its in-memory state) is gone.
func NewGetAnalysisResult(repo analysisrepo.Repository) gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID := c.Param("id")

		result, err := repo.GetBySessionID(c.Request.Context(), sessionID)
		if errors.Is(err, analysisrepo.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "analysis result not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch analysis result"})
			return
		}

		c.JSON(http.StatusOK, result)
	}
}
