package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/httpserver"
)

func TestServer_Routes(t *testing.T) {
	fakeDetector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(detector.Result{
			Video:     "video.mp4",
			FakeScore: 0.9,
			Verdict:   "fake",
		})
	}))
	defer fakeDetector.Close()

	fakeAudioDetector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(audio.Result{
			DurationSeconds: 4.6,
			SampleRate:      16000,
			Channels:        1,
			Chunks:          2,
			Status:          "processed",
			FakeScore:       0.97,
			Verdict:         "fake",
		})
	}))
	defer fakeAudioDetector.Close()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := httpserver.New(config.Config{
		Port:                 "0",
		Environment:          "test",
		DetectorBaseURL:      fakeDetector.URL,
		DetectorTimeout:      5 * time.Second,
		AudioDetectorBaseURL: fakeAudioDetector.URL,
		AudioDetectorTimeout: 5 * time.Second,
		WorkerCount:          2,
		WorkerQueueSize:      8,
		RedisURL:             "redis://" + mr.Addr(),
		JobTTL:               time.Hour,
	}, logger)
	if err != nil {
		t.Fatalf("httpserver.New() error = %v", err)
	}
	defer srv.Pool.Shutdown(context.Background())
	defer srv.Redis.Close()

	ts := httptest.NewServer(srv.HTTP.Handler)
	defer ts.Close()

	// /health now actually pings Postgres and Redis (see
	// handlers.NewHealth) rather than always answering 200 — this test
	// doesn't configure a real DatabaseURL (that'd depend on whatever
	// Postgres happens to be reachable on the machine running the
	// tests), so it only checks the route is wired to the right handler
	// shape. The health-check logic itself — healthy/unhealthy for each
	// dependency independently — is covered deterministically with fakes
	// in internal/handlers/health_test.go.
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /health status = %d, want %d or %d", resp.StatusCode, http.StatusOK, http.StatusServiceUnavailable)
	}
	var health handlers.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode GET /health response: %v", err)
	}
	if _, ok := health.Checks["redis"]; !ok {
		t.Error(`GET /health response missing "redis" check`)
	}
	if health.Checks["redis"] != "healthy" {
		t.Errorf(`Checks["redis"] = %q, want "healthy" (a real miniredis is configured for this test)`, health.Checks["redis"])
	}

	resp2, err := http.Post(ts.URL+"/api/v1/analyze", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/v1/analyze: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /api/v1/analyze with no body status = %d, want %d", resp2.StatusCode, http.StatusBadRequest)
	}

	resp3, err := http.Post(
		ts.URL+"/api/v1/analyze",
		"application/json",
		strings.NewReader(`{"video_url":"https://example.com/video.mp4"}`),
	)
	if err != nil {
		t.Fatalf("POST /api/v1/analyze: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusAccepted {
		t.Errorf("POST /api/v1/analyze status = %d, want %d", resp3.StatusCode, http.StatusAccepted)
	}

	var queued handlers.AnalyzeResponse
	if err := json.NewDecoder(resp3.Body).Decode(&queued); err != nil {
		t.Fatalf("decode POST /api/v1/analyze response: %v", err)
	}
	if queued.ID == "" {
		t.Fatal("queued.ID is empty")
	}

	// Poll the status endpoint until the worker pool actually completes
	// the job — proves the full enqueue -> worker -> detector -> store
	// -> GET path works end to end, not just that POST returns 202.
	deadline := time.Now().Add(2 * time.Second)
	var status handlers.JobStatusResponse
	for time.Now().Before(deadline) {
		resp4, err := http.Get(ts.URL + "/api/v1/analyze/" + queued.ID)
		if err != nil {
			t.Fatalf("GET /api/v1/analyze/%s: %v", queued.ID, err)
		}
		if err := json.NewDecoder(resp4.Body).Decode(&status); err != nil {
			resp4.Body.Close()
			t.Fatalf("decode GET /api/v1/analyze/%s response: %v", queued.ID, err)
		}
		resp4.Body.Close()

		if status.Status == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if status.Status != "completed" {
		t.Fatalf("job status = %q, want %q (last seen: %+v)", status.Status, "completed", status)
	}
	if status.Result == nil || status.Result.Verdict != "fake" {
		t.Errorf("Result = %+v, want Verdict=fake", status.Result)
	}

	resp5, err := http.Get(ts.URL + "/api/v1/analyze/does-not-exist")
	if err != nil {
		t.Fatalf("GET /api/v1/analyze/does-not-exist: %v", err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/v1/analyze/does-not-exist status = %d, want %d", resp5.StatusCode, http.StatusNotFound)
	}

	// POST /api/v1/analyze-audio: real multipart upload through the real
	// server, proving the gateway -> Python audio-detector relay works
	// end to end, not just that the handler/client units work in isolation.
	audioBody := &bytes.Buffer{}
	audioWriter := multipart.NewWriter(audioBody)
	part, err := audioWriter.CreateFormFile("audio", "clip.wav")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("fake-wav-bytes")); err != nil {
		t.Fatalf("part.Write: %v", err)
	}
	if err := audioWriter.Close(); err != nil {
		t.Fatalf("audioWriter.Close: %v", err)
	}

	resp6, err := http.Post(ts.URL+"/api/v1/analyze-audio", audioWriter.FormDataContentType(), audioBody)
	if err != nil {
		t.Fatalf("POST /api/v1/analyze-audio: %v", err)
	}
	defer resp6.Body.Close()
	if resp6.StatusCode != http.StatusOK {
		t.Errorf("POST /api/v1/analyze-audio status = %d, want %d", resp6.StatusCode, http.StatusOK)
	}

	var audioResult audio.Result
	if err := json.NewDecoder(resp6.Body).Decode(&audioResult); err != nil {
		t.Fatalf("decode POST /api/v1/analyze-audio response: %v", err)
	}
	if audioResult.Verdict != "fake" || audioResult.Chunks != 2 {
		t.Errorf("audioResult = %+v, want Verdict=fake Chunks=2", audioResult)
	}
}
