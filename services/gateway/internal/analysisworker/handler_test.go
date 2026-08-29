package analysisworker_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/analysisworker"
	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
)

// fakeVideoAnalyzer stands in for *detector.Client.
type fakeVideoAnalyzer struct {
	result *detector.Result
	err    error
	gotURL string
}

func (f *fakeVideoAnalyzer) Analyze(ctx context.Context, videoURL string) (*detector.Result, error) {
	f.gotURL = videoURL
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeAudioAnalyzer stands in for *audio.Client.
type fakeAudioAnalyzer struct {
	result      *audio.Result
	err         error
	gotFilename string
	gotData     []byte
}

func (f *fakeAudioAnalyzer) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	f.gotFilename = filename
	f.gotData = data
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeAnalysisRepo is a minimal in-memory analysisrepo.Repository for
// handler-level tests — narrower than internal/handlers' fake since
// these tests only ever exercise the Upsert* methods.
type fakeAnalysisRepo struct {
	mu      sync.Mutex
	results map[string]analysisrepo.Result
	err     error
}

func newFakeAnalysisRepo() *fakeAnalysisRepo {
	return &fakeAnalysisRepo{results: make(map[string]analysisrepo.Result)}
}

func (f *fakeAnalysisRepo) Create(ctx context.Context, result analysisrepo.Result) error {
	panic("not used by these tests")
}

func (f *fakeAnalysisRepo) GetBySessionID(ctx context.Context, sessionID string) (*analysisrepo.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[sessionID]
	if !ok {
		return nil, analysisrepo.ErrNotFound
	}
	return &r, nil
}

func (f *fakeAnalysisRepo) UpsertVideoResult(ctx context.Context, sessionID string, score float64, verdict string, compute analysisrepo.ComputeRisk) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.results[sessionID]
	r.SessionID = sessionID
	r.VideoFakeScore = &score
	r.VideoVerdict = verdict
	r.RiskScore, r.RiskVerdict, r.RiskReasons = compute(r.VideoFakeScore, r.AudioFakeScore, r.TemporalScore)
	f.results[sessionID] = r
	return nil
}

func (f *fakeAnalysisRepo) UpsertAudioResult(ctx context.Context, sessionID string, score float64, verdict string, compute analysisrepo.ComputeRisk) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.results[sessionID]
	r.SessionID = sessionID
	r.AudioFakeScore = &score
	r.AudioVerdict = verdict
	r.RiskScore, r.RiskVerdict, r.RiskReasons = compute(r.VideoFakeScore, r.AudioFakeScore, r.TemporalScore)
	f.results[sessionID] = r
	return nil
}

func (f *fakeAnalysisRepo) get(sessionID string) (analysisrepo.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[sessionID]
	return r, ok
}

func TestVideoHandler_Handle_Success(t *testing.T) {
	det := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.87, Verdict: "fake"}}
	repo := newFakeAnalysisRepo()
	h := analysisworker.NewVideoHandler(det, repo)

	job, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}

	if err := h.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if det.gotURL != "https://example.com/video.mp4" {
		t.Errorf("detector received URL %q, want %q", det.gotURL, "https://example.com/video.mp4")
	}

	result, ok := repo.get("session-1")
	if !ok {
		t.Fatal("no result persisted for session-1")
	}
	if result.VideoFakeScore == nil || *result.VideoFakeScore != 0.87 || result.VideoVerdict != "fake" {
		t.Errorf("persisted video result = (%v, %q), want (0.87, fake)", result.VideoFakeScore, result.VideoVerdict)
	}
}

func TestVideoHandler_Handle_DetectorError(t *testing.T) {
	det := &fakeVideoAnalyzer{err: &detector.Error{Kind: detector.KindUnavailable, Message: "video-detector unreachable"}}
	h := analysisworker.NewVideoHandler(det, newFakeAnalysisRepo())

	job, _ := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	err := h.Handle(context.Background(), job)
	if err == nil {
		t.Fatal("Handle() error = nil, want the detector's error propagated")
	}
	if h.IsPermanent(err) {
		t.Error("IsPermanent(unavailable error) = true, want false — this should be retried")
	}
}

func TestVideoHandler_IsPermanent_InvalidVideo(t *testing.T) {
	h := analysisworker.NewVideoHandler(&fakeVideoAnalyzer{}, newFakeAnalysisRepo())

	err := &detector.Error{Kind: detector.KindInvalidVideo, Message: "unsupported format"}
	if !h.IsPermanent(err) {
		t.Error("IsPermanent(invalid video error) = false, want true — retrying malformed input can't succeed")
	}
}

func TestVideoHandler_Handle_RepositoryError(t *testing.T) {
	det := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.5, Verdict: "real"}}
	repo := newFakeAnalysisRepo()
	repo.err = errors.New("connection refused")
	h := analysisworker.NewVideoHandler(det, repo)

	job, _ := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err := h.Handle(context.Background(), job); err == nil {
		t.Fatal("Handle() error = nil, want the repository error propagated")
	}
}

// fakeAudioFetcher stands in for HTTPAudioFetcher.
type fakeAudioFetcher struct {
	data   []byte
	err    error
	gotURL string
}

func (f *fakeAudioFetcher) Fetch(ctx context.Context, audioURL string) ([]byte, error) {
	f.gotURL = audioURL
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

func TestAudioHandler_Handle_Success(t *testing.T) {
	fetcher := &fakeAudioFetcher{data: []byte("wav-bytes")}
	det := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.93, Verdict: "fake"}}
	repo := newFakeAnalysisRepo()
	h := analysisworker.NewAudioHandler(fetcher, det, repo)

	job, err := analysisjob.NewAudioAnalysisJob("session-2", "https://example.com/audio.wav")
	if err != nil {
		t.Fatalf("NewAudioAnalysisJob() error = %v", err)
	}

	if err := h.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if fetcher.gotURL != "https://example.com/audio.wav" {
		t.Errorf("fetcher received URL %q, want %q", fetcher.gotURL, "https://example.com/audio.wav")
	}
	if string(det.gotData) != "wav-bytes" {
		t.Errorf("detector received data %q, want %q", det.gotData, "wav-bytes")
	}

	result, ok := repo.get("session-2")
	if !ok {
		t.Fatal("no result persisted for session-2")
	}
	if result.AudioFakeScore == nil || *result.AudioFakeScore != 0.93 || result.AudioVerdict != "fake" {
		t.Errorf("persisted audio result = (%v, %q), want (0.93, fake)", result.AudioFakeScore, result.AudioVerdict)
	}
}

func TestAudioHandler_Handle_FetchError(t *testing.T) {
	fetcher := &fakeAudioFetcher{err: errors.New("404 not found")}
	h := analysisworker.NewAudioHandler(fetcher, &fakeAudioAnalyzer{}, newFakeAnalysisRepo())

	job, _ := analysisjob.NewAudioAnalysisJob("session-2", "https://example.com/missing.wav")
	if err := h.Handle(context.Background(), job); err == nil {
		t.Fatal("Handle() error = nil, want the fetch error propagated")
	}
}

func TestAudioHandler_IsPermanent_InvalidAudio(t *testing.T) {
	h := analysisworker.NewAudioHandler(&fakeAudioFetcher{}, &fakeAudioAnalyzer{}, newFakeAnalysisRepo())

	err := &audio.Error{Kind: audio.KindInvalidAudio, Message: "corrupt file"}
	if !h.IsPermanent(err) {
		t.Error("IsPermanent(invalid audio error) = false, want true")
	}
}

// TestHTTPAudioFetcher_Fetch proves the real fetcher (not a fake) works
// against a real HTTP server — the one piece of handler.go that isn't
// "reuse the existing client," so it earns its own live test.
func TestHTTPAudioFetcher_Fetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("real audio bytes"))
	}))
	defer srv.Close()

	fetcher := analysisworker.NewHTTPAudioFetcher()
	data, err := fetcher.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if string(data) != "real audio bytes" {
		t.Errorf("Fetch() = %q, want %q", data, "real audio bytes")
	}
}

func TestHTTPAudioFetcher_Fetch_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	fetcher := analysisworker.NewHTTPAudioFetcher()
	if _, err := fetcher.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("Fetch() error = nil, want an error for a 404 response")
	}
}
