package handlers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
)

// DetectorClient analyzes a video and returns the video-detector's result.
type DetectorClient interface {
	Analyze(ctx context.Context, videoURL string) (*detector.Result, error)
}

// AnalyzeRequest is the JSON body accepted by the analyze endpoint.
type AnalyzeRequest struct {
	VideoURL string `json:"video_url" binding:"required,url"`
}

// AnalyzeResponse is the JSON body returned once analysis has completed.
type AnalyzeResponse struct {
	ID       string          `json:"id"`
	VideoURL string          `json:"video_url"`
	Result   detector.Result `json:"result"`
}

// NewAnalyze builds the analyze handler backed by the given video-detector client.
func NewAnalyze(client DetectorClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req AnalyzeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		id, err := newRequestID()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create request id"})
			return
		}

		result, err := client.Analyze(c.Request.Context(), req.VideoURL)
		if err != nil {
			status, message := detectorErrorResponse(err)
			c.JSON(status, gin.H{"id": id, "error": message})
			return
		}

		c.JSON(http.StatusOK, AnalyzeResponse{
			ID:       id,
			VideoURL: req.VideoURL,
			Result:   *result,
		})
	}
}

func detectorErrorResponse(err error) (int, string) {
	var derr *detector.Error
	if errors.As(err, &derr) {
		switch derr.Kind {
		case detector.KindInvalidVideo:
			return http.StatusUnprocessableEntity, derr.Message
		case detector.KindTimeout:
			return http.StatusGatewayTimeout, "video-detector timed out"
		case detector.KindUnavailable:
			return http.StatusBadGateway, "video-detector is unavailable"
		}
	}
	return http.StatusBadGateway, "video-detector failed to analyze video"
}

// newRequestID generates a random UUID v4 string.
func newRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
