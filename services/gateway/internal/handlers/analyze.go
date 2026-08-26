package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

// JobSubmitter enqueues a video analysis job. worker.Pool implements it.
type JobSubmitter interface {
	Submit(videoURL string) (worker.Job, error)
}

// AnalyzeRequest is the JSON body accepted by the analyze endpoint.
type AnalyzeRequest struct {
	VideoURL string `json:"video_url" binding:"required,url"`
}

// AnalyzeResponse is the JSON body returned once a job has been queued.
// Poll GET /api/v1/analyze/:id for its result.
type AnalyzeResponse struct {
	ID       string `json:"id"`
	VideoURL string `json:"video_url"`
	Status   string `json:"status"`
}

// NewAnalyze builds the analyze handler backed by the given job
// submitter. It enqueues the request and returns immediately — it never
// waits for the video-detector itself.
func NewAnalyze(submitter JobSubmitter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req AnalyzeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		job, err := submitter.Submit(req.VideoURL)
		if err != nil {
			status, message := submitErrorResponse(err)
			c.JSON(status, gin.H{"id": job.ID, "error": message})
			return
		}

		c.JSON(http.StatusAccepted, AnalyzeResponse{
			ID:       job.ID,
			VideoURL: job.VideoURL,
			Status:   string(job.Status),
		})
	}
}

func submitErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, worker.ErrQueueFull):
		return http.StatusServiceUnavailable, "job queue is full, try again shortly"
	case errors.Is(err, worker.ErrPoolClosed):
		return http.StatusServiceUnavailable, "server is shutting down"
	case errors.Is(err, worker.ErrRedisUnavailable):
		return http.StatusServiceUnavailable, "job queue is temporarily unavailable"
	default:
		return http.StatusInternalServerError, "failed to create job"
	}
}
