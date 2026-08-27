package handlers

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/session"
)

// maxSessionAudioBytes bounds a session's audio upload — same cap as
// the standalone /api/v1/analyze-audio endpoint.
const maxSessionAudioBytes = 25 << 20 // 25MiB

// SessionAnalyzer runs a combined video+audio analysis session.
// session.Service implements it.
type SessionAnalyzer interface {
	Analyze(ctx context.Context, req session.AnalyzeRequest) (*session.AnalysisSession, error)
}

// RiskAssessor turns a completed analysis session into a risk
// assessment. *risk.Engine implements it.
type RiskAssessor interface {
	Assess(s *session.AnalysisSession) risk.Assessment
}

// analyzeSessionResponse is the /api/v1/analyze-session response body:
// the session's own fields (id, status, video, audio, ...) with the
// risk assessment computed from them added alongside. The embedded
// pointer promotes AnalysisSession's fields to the top level of the
// JSON object rather than nesting them under a "session" key.
type analyzeSessionResponse struct {
	*session.AnalysisSession
	Risk riskView `json:"risk"`
}

// riskView is the risk assessment as exposed over the API: the
// modality scores are omitted since they're already present as
// video.fake_score/audio.fake_score at the top level of the response.
type riskView struct {
	RiskScore float64      `json:"risk_score"`
	Verdict   risk.Verdict `json:"verdict"`
	Reasons   []string     `json:"reasons"`
}

func newRiskView(a risk.Assessment) riskView {
	return riskView{RiskScore: a.RiskScore, Verdict: a.Verdict, Reasons: a.Reasons}
}

// NewAnalyzeSession builds the /api/v1/analyze-session handler backed by
// the given session service and risk engine. It's a multipart request
// carrying an optional "video_url" form field and an optional "audio"
// file field — at least one of the two is required. Video and audio are
// analyzed concurrently (see internal/session for how); once both
// finish, the resulting session is fed to riskEngine to compute the
// combined risk assessment returned alongside it.
func NewAnalyzeSession(svc SessionAnalyzer, riskEngine RiskAssessor) gin.HandlerFunc {
	return func(c *gin.Context) {
		videoURL := c.PostForm("video_url")
		if videoURL != "" {
			if _, err := url.ParseRequestURI(videoURL); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "video_url is not a valid URL"})
				return
			}
		}

		var audioFilename string
		var audioData []byte

		if fileHeader, ferr := c.FormFile("audio"); ferr == nil {
			if fileHeader.Size > maxSessionAudioBytes {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "audio file too large"})
				return
			}

			file, err := fileHeader.Open()
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read audio file"})
				return
			}
			defer file.Close()

			data, err := io.ReadAll(io.LimitReader(file, maxSessionAudioBytes+1))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read audio file"})
				return
			}

			audioFilename = fileHeader.Filename
			audioData = data
		}

		if videoURL == "" && len(audioData) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "at least one of video_url or audio is required"})
			return
		}

		result, err := svc.Analyze(c.Request.Context(), session.AnalyzeRequest{
			VideoURL:      videoURL,
			AudioFilename: audioFilename,
			AudioData:     audioData,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create analysis session"})
			return
		}

		assessment := riskEngine.Assess(result)
		c.JSON(http.StatusOK, analyzeSessionResponse{
			AnalysisSession: result,
			Risk:            newRiskView(assessment),
		})
	}
}
