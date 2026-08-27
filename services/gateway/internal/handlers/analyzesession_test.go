package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/session"
)

type fakeSessionAnalyzer struct {
	called bool
	req    session.AnalyzeRequest
	result *session.AnalysisSession
	err    error
}

func (f *fakeSessionAnalyzer) Analyze(ctx context.Context, req session.AnalyzeRequest) (*session.AnalysisSession, error) {
	f.called = true
	f.req = req
	return f.result, f.err
}

// sessionResponse mirrors the handler's JSON response shape: the
// session's own fields flattened at the top level, plus the nested risk
// assessment.
type sessionResponse struct {
	session.AnalysisSession
	Risk struct {
		RiskScore float64  `json:"risk_score"`
		Verdict   string   `json:"verdict"`
		Reasons   []string `json:"reasons"`
	} `json:"risk"`
}

func decodeSessionResponse(t *testing.T, rec *httptest.ResponseRecorder) sessionResponse {
	t.Helper()
	var body sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v (body = %s)", err, rec.Body.String())
	}
	return body
}

// newAnalyzeSessionRouter wires the handler with a real risk.Engine
// (the default 50/50 weights and thresholds), so these tests exercise
// the actual Session Service → Risk Engine wiring end to end rather than
// a stubbed-out assessment.
func newAnalyzeSessionRouter(svc handlers.SessionAnalyzer) *gin.Engine {
	return newAnalyzeSessionRouterWithEngine(svc, risk.NewEngine())
}

func newAnalyzeSessionRouterWithEngine(svc handlers.SessionAnalyzer, riskEngine handlers.RiskAssessor) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/analyze-session", handlers.NewAnalyzeSession(svc, riskEngine))
	return router
}

func doAnalyzeSession(t *testing.T, router *gin.Engine, videoURL, audioFilename string, audioContent []byte) *httptest.ResponseRecorder {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if videoURL != "" {
		if err := writer.WriteField("video_url", videoURL); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	if audioFilename != "" {
		part, err := writer.CreateFormFile("audio", audioFilename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(audioContent); err != nil {
			t.Fatalf("part.Write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyze-session", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAnalyzeSession_Success(t *testing.T) {
	fake := &fakeSessionAnalyzer{result: &session.AnalysisSession{
		ID:     "session-123",
		Status: session.StatusCompleted,
		Video:  &session.VideoResult{FakeScore: 0.08, Verdict: "real"},
		Audio:  &session.AudioResult{FakeScore: 0.91, Verdict: "fake"},
	}}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "https://example.com/clip.mp4", "clip.wav", []byte("wav-bytes"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !fake.called {
		t.Fatal("expected SessionAnalyzer.Analyze to be called")
	}
	if fake.req.VideoURL != "https://example.com/clip.mp4" {
		t.Errorf("req.VideoURL = %q, want %q", fake.req.VideoURL, "https://example.com/clip.mp4")
	}
	if fake.req.AudioFilename != "clip.wav" || string(fake.req.AudioData) != "wav-bytes" {
		t.Errorf("req audio = %q/%q, want clip.wav/wav-bytes", fake.req.AudioFilename, fake.req.AudioData)
	}

	body := decodeSessionResponse(t, rec)
	if body.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", body.Status, session.StatusCompleted)
	}
	if body.Video == nil || body.Video.Verdict != "real" {
		t.Errorf("Video = %+v, want Verdict=real", body.Video)
	}
	if body.Audio == nil || body.Audio.Verdict != "fake" {
		t.Errorf("Audio = %+v, want Verdict=fake", body.Audio)
	}

	// (0.08*0.5 + 0.91*0.5) = 0.495, at the default 50/50 weights.
	const wantRiskScore = 0.495
	if diff := body.Risk.RiskScore - wantRiskScore; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Risk.RiskScore = %v, want %v", body.Risk.RiskScore, wantRiskScore)
	}
	if body.Risk.Verdict != string(risk.VerdictSuspicious) {
		t.Errorf("Risk.Verdict = %q, want %q", body.Risk.Verdict, risk.VerdictSuspicious)
	}
	if len(body.Risk.Reasons) == 0 {
		t.Error("Risk.Reasons is empty, want at least one reason for the high audio score")
	}
}

func TestAnalyzeSession_VideoOnly(t *testing.T) {
	fake := &fakeSessionAnalyzer{result: &session.AnalysisSession{ID: "s1", Status: session.StatusCompleted}}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "https://example.com/clip.mp4", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fake.req.AudioFilename != "" || fake.req.AudioData != nil {
		t.Errorf("req audio = %q/%v, want both empty", fake.req.AudioFilename, fake.req.AudioData)
	}
}

func TestAnalyzeSession_AudioOnly(t *testing.T) {
	fake := &fakeSessionAnalyzer{result: &session.AnalysisSession{ID: "s1", Status: session.StatusCompleted}}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "", "clip.wav", []byte("data"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fake.req.VideoURL != "" {
		t.Errorf("req.VideoURL = %q, want empty", fake.req.VideoURL)
	}
}

func TestAnalyzeSession_NeitherProvided(t *testing.T) {
	fake := &fakeSessionAnalyzer{}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "", "", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if fake.called {
		t.Error("Analyze should not be called when neither video nor audio is provided")
	}
}

func TestAnalyzeSession_InvalidVideoURL(t *testing.T) {
	fake := &fakeSessionAnalyzer{}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "not-a-url", "", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if fake.called {
		t.Error("Analyze should not be called for an invalid video_url")
	}
}

func TestAnalyzeSession_TooLargeAudio(t *testing.T) {
	fake := &fakeSessionAnalyzer{}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "", "big.wav", bytes.Repeat([]byte("x"), 25<<20+1))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if fake.called {
		t.Error("Analyze should not be called for an oversized audio file")
	}
}

func TestAnalyzeSession_PartialResultReturns200(t *testing.T) {
	// A partial result (one side failed) is still a successful HTTP
	// response — the failure is reported inside the body, not via
	// status code, since the session itself was created successfully.
	fake := &fakeSessionAnalyzer{result: &session.AnalysisSession{
		ID:         "s1",
		Status:     session.StatusPartial,
		Audio:      &session.AudioResult{FakeScore: 0.2, Verdict: "real"},
		VideoError: "video detector unreachable",
	}}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "https://example.com/clip.mp4", "clip.wav", []byte("data"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeSessionResponse(t, rec)
	if body.Status != session.StatusPartial {
		t.Errorf("Status = %q, want %q", body.Status, session.StatusPartial)
	}
	if body.VideoError != "video detector unreachable" {
		t.Errorf("VideoError = %q, want %q", body.VideoError, "video detector unreachable")
	}
}

// TestAnalyzeSession_Risk_VideoOnlySucceeded proves that when audio
// failed (or wasn't requested) but video succeeded, the risk assessment
// is computed from the video signal alone rather than treating the
// missing audio score as 0 ("safe").
func TestAnalyzeSession_Risk_VideoOnlySucceeded(t *testing.T) {
	fake := &fakeSessionAnalyzer{result: &session.AnalysisSession{
		ID:         "s1",
		Status:     session.StatusPartial,
		Video:      &session.VideoResult{FakeScore: 0.7, Verdict: "fake"},
		AudioError: "audio detector unreachable",
	}}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "https://example.com/clip.mp4", "clip.wav", []byte("data"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeSessionResponse(t, rec)
	const wantRiskScore = 0.7
	if diff := body.Risk.RiskScore - wantRiskScore; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Risk.RiskScore = %v, want %v (video score alone)", body.Risk.RiskScore, wantRiskScore)
	}
	if body.Risk.Verdict != string(risk.VerdictLikelyFake) {
		t.Errorf("Risk.Verdict = %q, want %q", body.Risk.Verdict, risk.VerdictLikelyFake)
	}
}

// TestAnalyzeSession_Risk_AudioOnlySucceeded is the mirror of the
// video-only case above: video failed, so risk comes from audio alone.
func TestAnalyzeSession_Risk_AudioOnlySucceeded(t *testing.T) {
	fake := &fakeSessionAnalyzer{result: &session.AnalysisSession{
		ID:         "s1",
		Status:     session.StatusPartial,
		Audio:      &session.AudioResult{FakeScore: 0.2, Verdict: "real"},
		VideoError: "video detector unreachable",
	}}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "https://example.com/clip.mp4", "clip.wav", []byte("data"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeSessionResponse(t, rec)
	const wantRiskScore = 0.2
	if diff := body.Risk.RiskScore - wantRiskScore; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Risk.RiskScore = %v, want %v (audio score alone)", body.Risk.RiskScore, wantRiskScore)
	}
	if body.Risk.Verdict != string(risk.VerdictLikelyAuthentic) {
		t.Errorf("Risk.Verdict = %q, want %q", body.Risk.Verdict, risk.VerdictLikelyAuthentic)
	}
}

// TestAnalyzeSession_Risk_BothFailed proves that when neither modality
// produced a usable signal, the risk verdict is UNKNOWN rather than a
// misleadingly confident score.
func TestAnalyzeSession_Risk_BothFailed(t *testing.T) {
	fake := &fakeSessionAnalyzer{result: &session.AnalysisSession{
		ID:         "s1",
		Status:     session.StatusFailed,
		VideoError: "video detector unreachable",
		AudioError: "audio detector unreachable",
	}}
	router := newAnalyzeSessionRouter(fake)

	rec := doAnalyzeSession(t, router, "https://example.com/clip.mp4", "clip.wav", []byte("data"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeSessionResponse(t, rec)
	if body.Risk.Verdict != string(risk.VerdictUnknown) {
		t.Errorf("Risk.Verdict = %q, want %q", body.Risk.Verdict, risk.VerdictUnknown)
	}
	if body.Risk.RiskScore != 0 {
		t.Errorf("Risk.RiskScore = %v, want 0", body.Risk.RiskScore)
	}
	if len(body.Risk.Reasons) == 0 {
		t.Error("Risk.Reasons is empty, want reasons explaining both failures")
	}
}

// fakeRiskEngine lets a test assert exactly what the handler passed to
// the risk engine, decoupled from the real Engine's scoring logic.
type fakeRiskEngine struct {
	called bool
	got    *session.AnalysisSession
	result risk.Assessment
}

func (f *fakeRiskEngine) Assess(s *session.AnalysisSession) risk.Assessment {
	f.called = true
	f.got = s
	return f.result
}

// TestAnalyzeSession_PassesCompletedSessionToRiskEngine proves the
// handler feeds the risk engine the exact AnalysisSession the session
// service produced — the "AnalysisSession → Risk Engine" step of the
// pipeline — rather than reconstructing or partially copying it.
func TestAnalyzeSession_PassesCompletedSessionToRiskEngine(t *testing.T) {
	completed := &session.AnalysisSession{
		ID:     "s1",
		Status: session.StatusCompleted,
		Video:  &session.VideoResult{FakeScore: 0.08, Verdict: "real"},
		Audio:  &session.AudioResult{FakeScore: 0.91, Verdict: "fake"},
	}
	svc := &fakeSessionAnalyzer{result: completed}
	riskEngine := &fakeRiskEngine{result: risk.Assessment{
		RiskScore: 0.42,
		Verdict:   risk.VerdictSuspicious,
		Reasons:   []string{"stubbed reason"},
	}}
	router := newAnalyzeSessionRouterWithEngine(svc, riskEngine)

	rec := doAnalyzeSession(t, router, "https://example.com/clip.mp4", "clip.wav", []byte("data"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if !riskEngine.called {
		t.Fatal("expected RiskAssessor.Assess to be called")
	}
	if riskEngine.got != completed {
		t.Errorf("Assess called with %+v, want the exact session the service returned", riskEngine.got)
	}

	body := decodeSessionResponse(t, rec)
	if body.Risk.RiskScore != 0.42 {
		t.Errorf("Risk.RiskScore = %v, want 0.42", body.Risk.RiskScore)
	}
	if body.Risk.Verdict != string(risk.VerdictSuspicious) {
		t.Errorf("Risk.Verdict = %q, want %q", body.Risk.Verdict, risk.VerdictSuspicious)
	}
	if len(body.Risk.Reasons) != 1 || body.Risk.Reasons[0] != "stubbed reason" {
		t.Errorf("Risk.Reasons = %v, want [stubbed reason]", body.Risk.Reasons)
	}
}
