package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

// JobLookup finds a previously submitted job by ID. worker.Store
// implements it.
type JobLookup interface {
	Get(ctx context.Context, id string) (worker.Job, error)
}

// JobStatusResponse is the JSON body returned by GET /api/v1/analyze/:id.
// Result is present once Status is "completed"; Error is present once
// Status is "failed".
type JobStatusResponse struct {
	ID       string           `json:"id"`
	VideoURL string           `json:"video_url"`
	Status   string           `json:"status"`
	Attempts int              `json:"attempts"`
	Result   *detector.Result `json:"result,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// NewJobStatus builds the GET /api/v1/analyze/:id handler backed by the
// given job lookup.
func NewJobStatus(lookup JobLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		job, err := lookup.Get(c.Request.Context(), id)
		if err != nil {
			switch {
			case errors.Is(err, worker.ErrJobNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
			case errors.Is(err, worker.ErrRedisUnavailable):
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "job store is temporarily unavailable"})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up job"})
			}
			return
		}

		c.JSON(http.StatusOK, JobStatusResponse{
			ID:       job.ID,
			VideoURL: job.VideoURL,
			Status:   string(job.Status),
			Attempts: job.Attempts,
			Result:   job.Result,
			Error:    job.Error,
		})
	}
}
