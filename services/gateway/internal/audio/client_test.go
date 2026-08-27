package audio_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
)

func TestAnalyze_Success(t *testing.T) {
	want := audio.Result{
		DurationSeconds: 12.4,
		SampleRate:      16000,
		Channels:        1,
		Chunks:          4,
		Status:          "processed",
		FakeScore:       0.96,
		Verdict:         "fake",
	}

	var gotContentType string
	var gotFilename string
	var gotFileBytes []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze-audio" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotContentType = r.Header.Get("Content-Type")

		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("audio")
		if err != nil {
			t.Fatalf("FormFile(audio): %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename
		gotFileBytes, _ = io.ReadAll(file)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()

	client := audio.NewClient(server.URL, time.Second)
	got, err := client.Analyze(context.Background(), "clip.wav", []byte("fake-wav-bytes"))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if *got != want {
		t.Errorf("Analyze() = %+v, want %+v", *got, want)
	}

	if gotFilename != "clip.wav" {
		t.Errorf("uploaded filename = %q, want %q", gotFilename, "clip.wav")
	}
	if string(gotFileBytes) != "fake-wav-bytes" {
		t.Errorf("uploaded file content = %q, want %q", gotFileBytes, "fake-wav-bytes")
	}
	if got, want := gotContentType[:len("multipart/form-data")], "multipart/form-data"; got != want {
		t.Errorf("Content-Type = %q, want prefix %q", gotContentType, want)
	}
}

func TestAnalyze_InvalidAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"detail": "could not decode WAV audio"})
	}))
	defer server.Close()

	client := audio.NewClient(server.URL, time.Second)
	_, err := client.Analyze(context.Background(), "broken.wav", []byte("not-audio"))

	var aerr *audio.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("Analyze() error = %v, want *audio.Error", err)
	}
	if aerr.Kind != audio.KindInvalidAudio {
		t.Errorf("Kind = %v, want KindInvalidAudio", aerr.Kind)
	}
	if aerr.Message != "could not decode WAV audio" {
		t.Errorf("Message = %q, want %q", aerr.Message, "could not decode WAV audio")
	}
}

func TestAnalyze_DetectorError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"detail":"model crashed"}`))
	}))
	defer server.Close()

	client := audio.NewClient(server.URL, time.Second)
	_, err := client.Analyze(context.Background(), "clip.wav", []byte("data"))

	var aerr *audio.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("Analyze() error = %v, want *audio.Error", err)
	}
	if aerr.Kind != audio.KindDetectorError {
		t.Errorf("Kind = %v, want KindDetectorError", aerr.Kind)
	}
}

func TestAnalyze_Unavailable(t *testing.T) {
	client := audio.NewClient("http://127.0.0.1:1", time.Second)
	_, err := client.Analyze(context.Background(), "clip.wav", []byte("data"))

	var aerr *audio.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("Analyze() error = %v, want *audio.Error", err)
	}
	if aerr.Kind != audio.KindUnavailable {
		t.Errorf("Kind = %v, want KindUnavailable", aerr.Kind)
	}
}

func TestAnalyze_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := audio.NewClient(server.URL, 5*time.Millisecond)
	_, err := client.Analyze(context.Background(), "clip.wav", []byte("data"))

	var aerr *audio.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("Analyze() error = %v, want *audio.Error", err)
	}
	if aerr.Kind != audio.KindTimeout {
		t.Errorf("Kind = %v, want KindTimeout", aerr.Kind)
	}
}

func TestAnalyze_MalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	client := audio.NewClient(server.URL, time.Second)
	_, err := client.Analyze(context.Background(), "clip.wav", []byte("data"))

	var aerr *audio.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("Analyze() error = %v, want *audio.Error", err)
	}
	if aerr.Kind != audio.KindDetectorError {
		t.Errorf("Kind = %v, want KindDetectorError", aerr.Kind)
	}
}
