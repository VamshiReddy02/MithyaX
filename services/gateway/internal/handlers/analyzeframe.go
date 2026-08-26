package handlers

import (
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
)

// maxFrameBytes bounds a single JPEG snapshot upload — this is one still
// frame, not a video.
const maxFrameBytes = 2 << 20 // 2MiB

// FrameDetectorClient analyzes a single video frame and returns the
// video-detector's result.
type FrameDetectorClient interface {
	AnalyzeFrame(ctx context.Context, jpeg []byte) (*detector.FrameResult, error)
}

// NewAnalyzeFrame builds the /api/v1/analyze-frame handler backed by the
// given video-detector client. It accepts a raw JPEG body (as produced by
// a browser's canvas.toBlob) and relays the per-frame verdict.
func NewAnalyzeFrame(client FrameDetectorClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxFrameBytes+1))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read frame"})
			return
		}
		if len(body) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "empty frame"})
			return
		}
		if len(body) > maxFrameBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "frame too large"})
			return
		}

		result, err := client.AnalyzeFrame(c.Request.Context(), body)
		if err != nil {
			status, message := detectorErrorResponse(err)
			c.JSON(status, gin.H{"error": message})
			return
		}

		c.JSON(http.StatusOK, result)
	}
}
