package httpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

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

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httpserver.New(config.Config{
		Port:            "0",
		Environment:     "test",
		DetectorBaseURL: fakeDetector.URL,
		DetectorTimeout: 5 * time.Second,
		WorkerCount:     2,
		WorkerQueueSize: 8,
		RedisAddr:       mr.Addr(),
		JobTTL:          time.Hour,
	}, logger)
	defer srv.Pool.Shutdown(context.Background())
	defer srv.Redis.Close()

	ts := httptest.NewServer(srv.HTTP.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health status = %d, want %d", resp.StatusCode, http.StatusOK)
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
}
