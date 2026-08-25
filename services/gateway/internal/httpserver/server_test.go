package httpserver_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/config"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httpserver.New(config.Config{
		Port:            "0",
		Environment:     "test",
		DetectorBaseURL: fakeDetector.URL,
		DetectorTimeout: 5 * time.Second,
	}, logger)

	ts := httptest.NewServer(srv.Handler)
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
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("POST /api/v1/analyze status = %d, want %d", resp3.StatusCode, http.StatusOK)
	}
}
