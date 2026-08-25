package detector_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
)

func TestAnalyze_Success(t *testing.T) {
	want := detector.Result{
		Video:           "clip.mp4",
		Frames:          120,
		FacesDetected:   100,
		FakeScore:       0.83,
		FakeMean:        0.80,
		FakeMedian:      0.85,
		FakeP75:         0.90,
		FakeP90:         0.95,
		FakeMax:         0.99,
		EmbeddingFrames: 100,
		Verdict:         "fake",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	got, err := client.Analyze(context.Background(), "https://example.com/clip.mp4")
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if *got != want {
		t.Errorf("Analyze() = %+v, want %+v", *got, want)
	}
}

func TestAnalyze_InvalidVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"detail": "could not open video"})
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	_, err := client.Analyze(context.Background(), "https://example.com/broken.mp4")

	var derr *detector.Error
	if !errors.As(err, &derr) {
		t.Fatalf("Analyze() error = %v, want *detector.Error", err)
	}
	if derr.Kind != detector.KindInvalidVideo {
		t.Errorf("Kind = %v, want KindInvalidVideo", derr.Kind)
	}
	if derr.Message != "could not open video" {
		t.Errorf("Message = %q, want %q", derr.Message, "could not open video")
	}
}

func TestAnalyze_DetectorError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"detail":"model crashed"}`))
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	_, err := client.Analyze(context.Background(), "https://example.com/clip.mp4")

	var derr *detector.Error
	if !errors.As(err, &derr) {
		t.Fatalf("Analyze() error = %v, want *detector.Error", err)
	}
	if derr.Kind != detector.KindDetectorError {
		t.Errorf("Kind = %v, want KindDetectorError", derr.Kind)
	}
}

func TestAnalyze_Unavailable(t *testing.T) {
	client := detector.NewClient("http://127.0.0.1:1", time.Second)
	_, err := client.Analyze(context.Background(), "https://example.com/clip.mp4")

	var derr *detector.Error
	if !errors.As(err, &derr) {
		t.Fatalf("Analyze() error = %v, want *detector.Error", err)
	}
	if derr.Kind != detector.KindUnavailable {
		t.Errorf("Kind = %v, want KindUnavailable", derr.Kind)
	}
}

func TestAnalyze_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, 5*time.Millisecond)
	_, err := client.Analyze(context.Background(), "https://example.com/clip.mp4")

	var derr *detector.Error
	if !errors.As(err, &derr) {
		t.Fatalf("Analyze() error = %v, want *detector.Error", err)
	}
	if derr.Kind != detector.KindTimeout {
		t.Errorf("Kind = %v, want KindTimeout", derr.Kind)
	}
}

func TestAnalyze_MalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	_, err := client.Analyze(context.Background(), "https://example.com/clip.mp4")

	var derr *detector.Error
	if !errors.As(err, &derr) {
		t.Fatalf("Analyze() error = %v, want *detector.Error", err)
	}
	if derr.Kind != detector.KindDetectorError {
		t.Errorf("Kind = %v, want KindDetectorError", derr.Kind)
	}
}
