package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
)

// maxAudioBytes bounds a single audio upload — generous compared to a
// video frame, since real recordings can run well past a minute.
const maxAudioBytes = 25 << 20 // 25MiB

// AudioDetectorClient analyzes an uploaded audio file and returns the
// audio-detector's voice-detection result.
type AudioDetectorClient interface {
	Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error)
}

// NewAnalyzeAudio builds the /api/v1/analyze-audio handler backed by the
// given audio-detector client. It accepts a multipart "audio" file field
// (matching the Python service's own contract) and relays the result.
func NewAnalyzeAudio(client AudioDetectorClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		fileHeader, err := c.FormFile("audio")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "audio file is required"})
			return
		}
		if fileHeader.Size > maxAudioBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "audio file too large"})
			return
		}

		file, err := fileHeader.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read audio file"})
			return
		}
		defer file.Close()

		data, err := io.ReadAll(io.LimitReader(file, maxAudioBytes+1))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read audio file"})
			return
		}

		result, err := client.Analyze(c.Request.Context(), fileHeader.Filename, data)
		if err != nil {
			status, message := audioErrorResponse(err)
			c.JSON(status, gin.H{"error": message})
			return
		}

		c.JSON(http.StatusOK, result)
	}
}

func audioErrorResponse(err error) (int, string) {
	var aerr *audio.Error
	if errors.As(err, &aerr) {
		switch aerr.Kind {
		case audio.KindInvalidAudio:
			return http.StatusUnprocessableEntity, aerr.Message
		case audio.KindTimeout:
			return http.StatusGatewayTimeout, "audio-detector timed out"
		case audio.KindUnavailable:
			return http.StatusBadGateway, "audio-detector is unavailable"
		}
	}
	return http.StatusBadGateway, "audio-detector failed to analyze audio"
}
