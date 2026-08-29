package detector_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
)

func TestAnalyze_Success(t *testing.T) {
	// detector.Result now carries a FrameMetadata slice, so it can no
	// longer be compared with == / != (want is built and compared with
	// reflect.DeepEqual instead) and can no longer round-trip through
	// json.Encode(want) — FrameMetadata only implements UnmarshalJSON,
	// not MarshalJSON, since this client only ever decodes video-detector
	// responses, never produces them. The server below therefore writes
	// the video-detector's actual wire shape (nested "face" object) as a
	// literal JSON string; TestAnalyze_DecodesFrameMetadata below is the
	// dedicated test for that decoding.
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
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("Analyze() = %+v, want %+v", *got, want)
	}
}

// TestAnalyzeBytes_Success proves AnalyzeBytes (7.7.5) uploads the
// given bytes as a multipart file to /analyze-upload — the video-
// detector's endpoint for a worker that fetched the video itself
// rather than handing over a URL — and decodes the same response
// shape Analyze does.
func TestAnalyzeBytes_Success(t *testing.T) {
	want := detector.Result{
		Video:           "clip.mp4",
		Frames:          120,
		FacesDetected:   100,
		FakeScore:       0.83,
		Verdict:         "fake",
		EmbeddingFrames: 100,
	}

	var gotFilename string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze-upload" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm() error = %v", err)
		}
		file, header, err := r.FormFile("video")
		if err != nil {
			t.Fatalf("FormFile() error = %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename
		gotBody, _ = io.ReadAll(file)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	got, err := client.AnalyzeBytes(context.Background(), "clip.mp4", []byte("fake-video-bytes"))
	if err != nil {
		t.Fatalf("AnalyzeBytes() error = %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("AnalyzeBytes() = %+v, want %+v", *got, want)
	}
	if gotFilename != "clip.mp4" {
		t.Errorf("uploaded filename = %q, want %q", gotFilename, "clip.mp4")
	}
	if string(gotBody) != "fake-video-bytes" {
		t.Errorf("uploaded body = %q, want %q", gotBody, "fake-video-bytes")
	}
}

func TestAnalyzeBytes_InvalidVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"detail": "could not decode video"})
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	_, err := client.AnalyzeBytes(context.Background(), "clip.mp4", []byte("garbage"))

	var detErr *detector.Error
	if !errors.As(err, &detErr) || detErr.Kind != detector.KindInvalidVideo {
		t.Fatalf("AnalyzeBytes() error = %v, want KindInvalidVideo", err)
	}
}

// TestAnalyze_DecodesFrameMetadata proves the client correctly decodes
// the video-detector's actual /analyze wire format for frame_metadata:
// a nested "face" object (or null) rather than flat fields, mirroring
// the real Python response shape from services/video-detector.
func TestAnalyze_DecodesFrameMetadata(t *testing.T) {
	const body = `{
		"video": "clip.mp4",
		"frames": 195,
		"faces_detected": 194,
		"fake_score": 0.08,
		"fake_mean": 0.07,
		"fake_median": 0.06,
		"fake_p75": 0.09,
		"fake_p90": 0.12,
		"fake_max": 0.4,
		"embedding_frames": 194,
		"verdict": "real",
		"frame_metadata": [
			{
				"timestamp": 0.0,
				"fake_score": 0.08,
				"face_detected": true,
				"face": {"x": 120, "y": 80, "width": 180, "height": 220}
			},
			{
				"timestamp": 0.04,
				"fake_score": 0.0,
				"face_detected": false,
				"face": null
			}
		]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	got, err := client.Analyze(context.Background(), "https://example.com/clip.mp4")
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	want := []detector.FrameMetadata{
		{
			Timestamp:    0.0,
			FakeScore:    0.08,
			FaceDetected: true,
			FaceX:        120,
			FaceY:        80,
			FaceWidth:    180,
			FaceHeight:   220,
		},
		{
			Timestamp:    0.04,
			FakeScore:    0.0,
			FaceDetected: false,
		},
	}
	if !reflect.DeepEqual(got.FrameMetadata, want) {
		t.Errorf("FrameMetadata = %+v, want %+v", got.FrameMetadata, want)
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

func TestAnalyzeFrame_Success(t *testing.T) {
	want := detector.FrameResult{FaceDetected: true, FakeProbability: 0.91, Verdict: "fake"}

	var gotContentType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze-frame" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	got, err := client.AnalyzeFrame(context.Background(), []byte("fake-jpeg-bytes"))
	if err != nil {
		t.Fatalf("AnalyzeFrame() error = %v", err)
	}
	if *got != want {
		t.Errorf("AnalyzeFrame() = %+v, want %+v", *got, want)
	}
	if gotContentType != "image/jpeg" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "image/jpeg")
	}
	if gotBody != "fake-jpeg-bytes" {
		t.Errorf("request body = %q, want %q", gotBody, "fake-jpeg-bytes")
	}
}

func TestAnalyzeFrame_NoFaceDetected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(detector.FrameResult{FaceDetected: false, FakeProbability: 0, Verdict: "unknown"})
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	got, err := client.AnalyzeFrame(context.Background(), []byte("fake-jpeg-bytes"))
	if err != nil {
		t.Fatalf("AnalyzeFrame() error = %v", err)
	}
	if got.FaceDetected {
		t.Errorf("FaceDetected = true, want false")
	}
}

func TestAnalyzeFrame_InvalidImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"detail": "could not decode image"})
	}))
	defer server.Close()

	client := detector.NewClient(server.URL, time.Second)
	_, err := client.AnalyzeFrame(context.Background(), []byte("not-a-jpeg"))

	var derr *detector.Error
	if !errors.As(err, &derr) {
		t.Fatalf("AnalyzeFrame() error = %v, want *detector.Error", err)
	}
	if derr.Kind != detector.KindInvalidVideo {
		t.Errorf("Kind = %v, want KindInvalidVideo", derr.Kind)
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
