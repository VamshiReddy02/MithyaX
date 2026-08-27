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

func newAnalyzeSessionRouter(svc handlers.SessionAnalyzer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/analyze-session", handlers.NewAnalyzeSession(svc))
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

	var body session.AnalysisSession
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", body.Status, session.StatusCompleted)
	}
	if body.Video == nil || body.Video.Verdict != "real" {
		t.Errorf("Video = %+v, want Verdict=real", body.Video)
	}
	if body.Audio == nil || body.Audio.Verdict != "fake" {
		t.Errorf("Audio = %+v, want Verdict=fake", body.Audio)
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

	var body session.AnalysisSession
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Status != session.StatusPartial {
		t.Errorf("Status = %q, want %q", body.Status, session.StatusPartial)
	}
	if body.VideoError != "video detector unreachable" {
		t.Errorf("VideoError = %q, want %q", body.VideoError, "video detector unreachable")
	}
}
