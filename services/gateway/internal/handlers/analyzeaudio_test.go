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

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
)

type fakeAudioDetectorClient struct {
	called   bool
	filename string
	data     []byte
	result   *audio.Result
	err      error
}

func (f *fakeAudioDetectorClient) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	f.called = true
	f.filename = filename
	f.data = data
	return f.result, f.err
}

func newAnalyzeAudioRouter(client handlers.AudioDetectorClient) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/analyze-audio", handlers.NewAnalyzeAudio(client))
	return router
}

func doAnalyzeAudio(t *testing.T, router *gin.Engine, filename string, content []byte, includeFile bool) *httptest.ResponseRecorder {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if includeFile {
		part, err := writer.CreateFormFile("audio", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyze-audio", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAnalyzeAudio_Success(t *testing.T) {
	fake := &fakeAudioDetectorClient{result: &audio.Result{
		DurationSeconds: 12.4,
		SampleRate:      16000,
		Channels:        1,
		Chunks:          4,
		Status:          "processed",
		FakeScore:       0.96,
		Verdict:         "fake",
	}}
	router := newAnalyzeAudioRouter(fake)

	rec := doAnalyzeAudio(t, router, "clip.wav", []byte("fake-wav-bytes"), true)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !fake.called {
		t.Error("expected AudioDetectorClient.Analyze to be called")
	}
	if fake.filename != "clip.wav" {
		t.Errorf("filename passed to client = %q, want %q", fake.filename, "clip.wav")
	}
	if string(fake.data) != "fake-wav-bytes" {
		t.Errorf("data passed to client = %q, want %q", fake.data, "fake-wav-bytes")
	}

	var body audio.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Verdict != "fake" || body.FakeScore != 0.96 || body.Chunks != 4 {
		t.Errorf("body = %+v, want Verdict=fake FakeScore=0.96 Chunks=4", body)
	}
}

func TestAnalyzeAudio_MissingFile(t *testing.T) {
	fake := &fakeAudioDetectorClient{}
	router := newAnalyzeAudioRouter(fake)

	rec := doAnalyzeAudio(t, router, "", nil, false)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if fake.called {
		t.Error("Analyze should not be called when no file is uploaded")
	}
}

func TestAnalyzeAudio_TooLarge(t *testing.T) {
	fake := &fakeAudioDetectorClient{}
	router := newAnalyzeAudioRouter(fake)

	rec := doAnalyzeAudio(t, router, "big.wav", bytes.Repeat([]byte("x"), 25<<20+1), true)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if fake.called {
		t.Error("Analyze should not be called for an oversized file")
	}
}

func TestAnalyzeAudio_DetectorInvalidAudio(t *testing.T) {
	fake := &fakeAudioDetectorClient{err: &audio.Error{Kind: audio.KindInvalidAudio, Message: "could not decode WAV audio"}}
	router := newAnalyzeAudioRouter(fake)

	rec := doAnalyzeAudio(t, router, "broken.wav", []byte("garbage"), true)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
}

func TestAnalyzeAudio_DetectorTimeout(t *testing.T) {
	fake := &fakeAudioDetectorClient{err: &audio.Error{Kind: audio.KindTimeout, Message: "timed out"}}
	router := newAnalyzeAudioRouter(fake)

	rec := doAnalyzeAudio(t, router, "clip.wav", []byte("data"), true)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusGatewayTimeout, rec.Body.String())
	}
}

func TestAnalyzeAudio_DetectorUnavailable(t *testing.T) {
	fake := &fakeAudioDetectorClient{err: &audio.Error{Kind: audio.KindUnavailable, Message: "unreachable"}}
	router := newAnalyzeAudioRouter(fake)

	rec := doAnalyzeAudio(t, router, "clip.wav", []byte("data"), true)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}

func TestAnalyzeAudio_DetectorError(t *testing.T) {
	fake := &fakeAudioDetectorClient{err: &audio.Error{Kind: audio.KindDetectorError, Message: "boom"}}
	router := newAnalyzeAudioRouter(fake)

	rec := doAnalyzeAudio(t, router, "clip.wav", []byte("data"), true)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}
