package analysisworker_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/analysisworker"
	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	jobsrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/jobs"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
)

// fakeVideoFetcher and fakeAudioFetcher both stand in for a
// *analysisworker.SafeURLFetcher — VideoHandler and AudioHandler now
// share the identical URLFetcher shape (7.7.5), fetching bytes before
// ever calling a detector. Kept as two distinct (if identical) types
// rather than one shared fake so each modality's tests read on their
// own terms — small duplication over an abstraction neither side
// actually needs.
type fakeVideoFetcher struct {
	data   []byte
	err    error
	gotURL string
}

func (f *fakeVideoFetcher) Fetch(ctx context.Context, videoURL string) ([]byte, error) {
	f.gotURL = videoURL
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

// fakeVideoAnalyzer stands in for *detector.Client's AnalyzeBytes.
type fakeVideoAnalyzer struct {
	result      *detector.Result
	err         error
	gotFilename string
	gotData     []byte
}

func (f *fakeVideoAnalyzer) AnalyzeBytes(ctx context.Context, filename string, data []byte) (*detector.Result, error) {
	f.gotFilename = filename
	f.gotData = data
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
	if compute != nil {
		r.RiskScore, r.RiskVerdict, r.RiskReasons = compute(r.VideoFakeScore, r.AudioFakeScore, r.TemporalScore)
	}
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
	if compute != nil {
		r.RiskScore, r.RiskVerdict, r.RiskReasons = compute(r.VideoFakeScore, r.AudioFakeScore, r.TemporalScore)
	}
	f.results[sessionID] = r
	return nil
}

func (f *fakeAnalysisRepo) FinalizeRisk(ctx context.Context, sessionID string, compute analysisrepo.ComputeRisk) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[sessionID]
	if !ok {
		return analysisrepo.ErrNotFound
	}
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

// noWaitCoordinator is a Coordinator backed by an empty fakeJobsRepo —
// GetLatestBySessionAndType always returns ErrNotFound, so
// ShouldFinalize always says "the other modality was never requested,"
// i.e. always ready to compute risk immediately. That's the right
// default for handler-level tests that only exercise a single
// modality in isolation; tests of the 7.6.6 wait/finalize behavior
// itself live in coordinator_test.go.
func noWaitCoordinator() *analysisworker.Coordinator {
	return analysisworker.NewCoordinator(newFakeJobsRepo())
}

func TestVideoHandler_Handle_Success(t *testing.T) {
	fetcher := &fakeVideoFetcher{data: []byte("video-bytes")}
	det := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.87, Verdict: "fake"}}
	repo := newFakeAnalysisRepo()
	h := analysisworker.NewVideoHandler(fetcher, det, repo, noWaitCoordinator())

	job, err := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}

	if err := h.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if fetcher.gotURL != "https://example.com/video.mp4" {
		t.Errorf("fetcher received URL %q, want %q", fetcher.gotURL, "https://example.com/video.mp4")
	}
	if det.gotFilename != "video.mp4" {
		t.Errorf("detector received filename %q, want %q", det.gotFilename, "video.mp4")
	}
	if string(det.gotData) != "video-bytes" {
		t.Errorf("detector received data %q, want %q", det.gotData, "video-bytes")
	}

	result, ok := repo.get("session-1")
	if !ok {
		t.Fatal("no result persisted for session-1")
	}
	if result.VideoFakeScore == nil || *result.VideoFakeScore != 0.87 || result.VideoVerdict != "fake" {
		t.Errorf("persisted video result = (%v, %q), want (0.87, fake)", result.VideoFakeScore, result.VideoVerdict)
	}
}

// TestVideoHandler_Handle_PreservesFilenameFromURL proves the file
// extension (a hint to the video-detector about the container format)
// survives the move from URL to bytes, rather than being flattened to
// a fixed name the way AudioHandler already does for audio.
func TestVideoHandler_Handle_PreservesFilenameFromURL(t *testing.T) {
	fetcher := &fakeVideoFetcher{data: []byte("video-bytes")}
	det := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.1, Verdict: "real"}}
	h := analysisworker.NewVideoHandler(fetcher, det, newFakeAnalysisRepo(), noWaitCoordinator())

	job, _ := analysisjob.NewVideoAnalysisJob("session-1", "https://cdn.example/clips/interview.mov")
	if err := h.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if det.gotFilename != "interview.mov" {
		t.Errorf("detector received filename %q, want %q", det.gotFilename, "interview.mov")
	}
}

func TestVideoHandler_Handle_FetchError(t *testing.T) {
	fetcher := &fakeVideoFetcher{err: errors.New("connection reset")}
	det := &fakeVideoAnalyzer{}
	h := analysisworker.NewVideoHandler(fetcher, det, newFakeAnalysisRepo(), noWaitCoordinator())

	job, _ := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err := h.Handle(context.Background(), job); err == nil {
		t.Fatal("Handle() error = nil, want the fetch error propagated")
	}
	if det.gotData != nil {
		t.Error("the video-detector was called despite the fetch failing")
	}
}

// TestVideoHandler_Handle_BlockedBySSRFValidation proves 7.7.5's core
// guarantee at the handler level: a fetch blocked by SafeFetcher's SSRF
// validation (surfaced here as a *security.FetchError, exactly what a
// real SafeURLFetcher would return) never reaches the video-detector,
// and is classified as permanent — retrying a blocked URL can't ever
// succeed.
func TestVideoHandler_Handle_BlockedBySSRFValidation(t *testing.T) {
	blockedErr := &security.FetchError{Kind: security.FetchErrorBlocked, Message: "blocked by SSRF validation"}
	fetcher := &fakeVideoFetcher{err: blockedErr}
	det := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.5, Verdict: "real"}}
	h := analysisworker.NewVideoHandler(fetcher, det, newFakeAnalysisRepo(), noWaitCoordinator())

	job, err := analysisjob.NewVideoAnalysisJob("session-blocked", "http://127.0.0.1/internal.mp4")
	if err != nil {
		t.Fatalf("NewVideoAnalysisJob() error = %v", err)
	}

	handleErr := h.Handle(context.Background(), job)
	if handleErr == nil {
		t.Fatal("Handle() error = nil, want the blocked fetch error propagated")
	}
	if det.gotData != nil {
		t.Error("the video-detector was called despite the URL failing SSRF validation")
	}
	if !h.IsPermanent(handleErr) {
		t.Error("IsPermanent() = false, want true — a blocked URL will never become fetchable by retrying")
	}
}

func TestVideoHandler_Handle_DetectorError(t *testing.T) {
	fetcher := &fakeVideoFetcher{data: []byte("video-bytes")}
	det := &fakeVideoAnalyzer{err: &detector.Error{Kind: detector.KindUnavailable, Message: "video-detector unreachable"}}
	h := analysisworker.NewVideoHandler(fetcher, det, newFakeAnalysisRepo(), noWaitCoordinator())

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
	h := analysisworker.NewVideoHandler(&fakeVideoFetcher{}, &fakeVideoAnalyzer{}, newFakeAnalysisRepo(), noWaitCoordinator())

	err := &detector.Error{Kind: detector.KindInvalidVideo, Message: "unsupported format"}
	if !h.IsPermanent(err) {
		t.Error("IsPermanent(invalid video error) = false, want true — retrying malformed input can't succeed")
	}
}

func TestVideoHandler_Handle_RepositoryError(t *testing.T) {
	fetcher := &fakeVideoFetcher{data: []byte("video-bytes")}
	det := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.5, Verdict: "real"}}
	repo := newFakeAnalysisRepo()
	repo.err = errors.New("connection refused")
	h := analysisworker.NewVideoHandler(fetcher, det, repo, noWaitCoordinator())

	job, _ := analysisjob.NewVideoAnalysisJob("session-1", "https://example.com/video.mp4")
	if err := h.Handle(context.Background(), job); err == nil {
		t.Fatal("Handle() error = nil, want the repository error propagated")
	}
}

// fakeAudioFetcher stands in for a *analysisworker.SafeURLFetcher.
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
	h := analysisworker.NewAudioHandler(fetcher, det, repo, noWaitCoordinator())

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
	h := analysisworker.NewAudioHandler(fetcher, &fakeAudioAnalyzer{}, newFakeAnalysisRepo(), noWaitCoordinator())

	job, _ := analysisjob.NewAudioAnalysisJob("session-2", "https://example.com/missing.wav")
	if err := h.Handle(context.Background(), job); err == nil {
		t.Fatal("Handle() error = nil, want the fetch error propagated")
	}
}

func TestAudioHandler_IsPermanent_InvalidAudio(t *testing.T) {
	h := analysisworker.NewAudioHandler(&fakeAudioFetcher{}, &fakeAudioAnalyzer{}, newFakeAnalysisRepo(), noWaitCoordinator())

	err := &audio.Error{Kind: audio.KindInvalidAudio, Message: "corrupt file"}
	if !h.IsPermanent(err) {
		t.Error("IsPermanent(invalid audio error) = false, want true")
	}
}

func TestAudioHandler_IsPermanent_BlockedBySSRFValidation(t *testing.T) {
	h := analysisworker.NewAudioHandler(&fakeAudioFetcher{}, &fakeAudioAnalyzer{}, newFakeAnalysisRepo(), noWaitCoordinator())

	err := &security.FetchError{Kind: security.FetchErrorBlocked, Message: "blocked by SSRF validation"}
	if !h.IsPermanent(err) {
		t.Error("IsPermanent(blocked fetch error) = false, want true")
	}
}

// TestVideoAndAudioHandlers_CombinedRisk_VideoFirst proves 7.6.6/7.6.7
// end to end at the handler level: video completing first, with audio
// still outstanding, must record only the video score (no risk yet);
// once audio completes, the coordinator sees video's job as terminal
// and the audio handler computes and stores the combined risk.
func TestVideoAndAudioHandlers_CombinedRisk_VideoFirst(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	coordinator := analysisworker.NewCoordinator(jobsRepo)
	analysisRepo := newFakeAnalysisRepo()

	videoJob, _ := analysisjob.NewVideoAnalysisJob("session-order", "https://example.com/v.mp4")
	audioJob, _ := analysisjob.NewAudioAnalysisJob("session-order", "https://example.com/a.wav")
	jobsRepo.put(jobsrepo.Job{ID: videoJob.ID, SessionID: "session-order", Type: string(analysisjob.TypeVideoAnalysis), Status: jobsrepo.StatusProcessing, CreatedAt: videoJob.CreatedAt})
	jobsRepo.put(jobsrepo.Job{ID: audioJob.ID, SessionID: "session-order", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusProcessing, CreatedAt: audioJob.CreatedAt})

	videoHandler := analysisworker.NewVideoHandler(&fakeVideoFetcher{data: []byte("v")}, &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.8, Verdict: "fake"}}, analysisRepo, coordinator)
	if err := videoHandler.Handle(context.Background(), videoJob); err != nil {
		t.Fatalf("video Handle() error = %v", err)
	}

	result, _ := analysisRepo.get("session-order")
	if result.RiskScore != 0 || result.RiskVerdict != "" {
		t.Errorf("risk computed after video alone = (%v, %q), want zero-value — audio is still outstanding", result.RiskScore, result.RiskVerdict)
	}

	// Video's own job is now done, from the coordinator's point of view —
	// mirrors what Worker.process's markCompleted does after a
	// successful Handle, which this handler-only test bypasses.
	jobsRepo.MarkCompleted(context.Background(), videoJob.ID)

	audioHandler := analysisworker.NewAudioHandler(&fakeAudioFetcher{data: []byte("wav")}, &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.9, Verdict: "fake"}}, analysisRepo, coordinator)
	if err := audioHandler.Handle(context.Background(), audioJob); err != nil {
		t.Fatalf("audio Handle() error = %v", err)
	}

	result, ok := analysisRepo.get("session-order")
	if !ok || result.RiskVerdict == "" {
		t.Fatalf("no risk computed after both modalities completed: %+v", result)
	}
	if result.VideoFakeScore == nil || *result.VideoFakeScore != 0.8 || result.AudioFakeScore == nil || *result.AudioFakeScore != 0.9 {
		t.Errorf("stored scores = video=%v audio=%v, want 0.8 and 0.9", result.VideoFakeScore, result.AudioFakeScore)
	}
}

// TestVideoAndAudioHandlers_CombinedRisk_AudioFirst is the mirror of
// the above with audio completing first — proving the result is
// genuinely order-independent, not incidentally video-first-only.
func TestVideoAndAudioHandlers_CombinedRisk_AudioFirst(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	coordinator := analysisworker.NewCoordinator(jobsRepo)
	analysisRepo := newFakeAnalysisRepo()

	videoJob, _ := analysisjob.NewVideoAnalysisJob("session-order-2", "https://example.com/v.mp4")
	audioJob, _ := analysisjob.NewAudioAnalysisJob("session-order-2", "https://example.com/a.wav")
	jobsRepo.put(jobsrepo.Job{ID: videoJob.ID, SessionID: "session-order-2", Type: string(analysisjob.TypeVideoAnalysis), Status: jobsrepo.StatusProcessing, CreatedAt: videoJob.CreatedAt})
	jobsRepo.put(jobsrepo.Job{ID: audioJob.ID, SessionID: "session-order-2", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusProcessing, CreatedAt: audioJob.CreatedAt})

	audioHandler := analysisworker.NewAudioHandler(&fakeAudioFetcher{data: []byte("wav")}, &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.9, Verdict: "fake"}}, analysisRepo, coordinator)
	if err := audioHandler.Handle(context.Background(), audioJob); err != nil {
		t.Fatalf("audio Handle() error = %v", err)
	}

	result, _ := analysisRepo.get("session-order-2")
	if result.RiskVerdict != "" {
		t.Errorf("risk computed after audio alone = %q, want empty — video is still outstanding", result.RiskVerdict)
	}

	// Audio's own job is now done — see the comment in the VideoFirst
	// test above for why this test does this by hand.
	jobsRepo.MarkCompleted(context.Background(), audioJob.ID)

	videoHandler := analysisworker.NewVideoHandler(&fakeVideoFetcher{data: []byte("v")}, &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.8, Verdict: "fake"}}, analysisRepo, coordinator)
	if err := videoHandler.Handle(context.Background(), videoJob); err != nil {
		t.Fatalf("video Handle() error = %v", err)
	}

	result, ok := analysisRepo.get("session-order-2")
	if !ok || result.RiskVerdict == "" {
		t.Fatalf("no risk computed after both modalities completed: %+v", result)
	}
}

// TestVideoHandler_OnDeadLetter_TriggersFinalizationWhenAudioDone
// proves 7.6.6's dead-letter wrinkle: a video job giving up permanently
// must still unblock a session whose audio already finished, or that
// session would never get a final risk assessment.
func TestVideoHandler_OnDeadLetter_TriggersFinalizationWhenAudioDone(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	coordinator := analysisworker.NewCoordinator(jobsRepo)
	analysisRepo := newFakeAnalysisRepo()

	videoJob, _ := analysisjob.NewVideoAnalysisJob("session-deadletter", "https://example.com/v.mp4")
	audioJob, _ := analysisjob.NewAudioAnalysisJob("session-deadletter", "https://example.com/a.wav")
	jobsRepo.put(jobsrepo.Job{ID: videoJob.ID, SessionID: "session-deadletter", Type: string(analysisjob.TypeVideoAnalysis), Status: jobsrepo.StatusDeadLetter, CreatedAt: videoJob.CreatedAt})
	jobsRepo.put(jobsrepo.Job{ID: audioJob.ID, SessionID: "session-deadletter", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusProcessing, CreatedAt: audioJob.CreatedAt})

	audioHandler := analysisworker.NewAudioHandler(&fakeAudioFetcher{data: []byte("wav")}, &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.6, Verdict: "suspicious"}}, analysisRepo, coordinator)
	if err := audioHandler.Handle(context.Background(), audioJob); err != nil {
		t.Fatalf("audio Handle() error = %v", err)
	}
	if result, _ := analysisRepo.get("session-deadletter"); result.RiskVerdict == "" {
		t.Fatal("expected risk already computed — video's job was already dead-lettered (terminal) before audio ran")
	}
}

// TestAudioHandler_OnDeadLetter_FinalizesWhenNoResultYet proves the
// OnDeadLetter path itself (not just the ordinary Handle path) invokes
// FinalizeRisk once the sibling is terminal — here video already
// completed and wrote a score, then audio permanently fails.
func TestAudioHandler_OnDeadLetter_FinalizesWhenNoResultYet(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	coordinator := analysisworker.NewCoordinator(jobsRepo)
	analysisRepo := newFakeAnalysisRepo()

	videoJob, _ := analysisjob.NewVideoAnalysisJob("session-audio-dl", "https://example.com/v.mp4")
	audioJob, _ := analysisjob.NewAudioAnalysisJob("session-audio-dl", "https://example.com/a.wav")
	jobsRepo.put(jobsrepo.Job{ID: videoJob.ID, SessionID: "session-audio-dl", Type: string(analysisjob.TypeVideoAnalysis), Status: jobsrepo.StatusCompleted, CreatedAt: videoJob.CreatedAt})
	jobsRepo.put(jobsrepo.Job{ID: audioJob.ID, SessionID: "session-audio-dl", Type: string(analysisjob.TypeAudioAnalysis), Status: jobsrepo.StatusDeadLetter, CreatedAt: audioJob.CreatedAt})

	videoHandler := analysisworker.NewVideoHandler(&fakeVideoFetcher{data: []byte("v")}, &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.3, Verdict: "real"}}, analysisRepo, coordinator)
	if err := videoHandler.Handle(context.Background(), videoJob); err != nil {
		t.Fatalf("video Handle() error = %v", err)
	}
	// At this point audio's job is already dead-lettered, so video's own
	// Handle should have finalized immediately via shouldComputeRisk.
	if result, _ := analysisRepo.get("session-audio-dl"); result.RiskVerdict == "" {
		t.Fatal("expected risk computed once video wrote its score, since audio was already terminal (dead_letter)")
	}

	audioHandler := analysisworker.NewAudioHandler(&fakeAudioFetcher{}, &fakeAudioAnalyzer{}, analysisRepo, coordinator)
	if err := audioHandler.OnDeadLetter(context.Background(), audioJob); err != nil {
		t.Fatalf("OnDeadLetter() error = %v", err)
	}
}

// fakeURLFetcher stands in for *security.SafeFetcher — SafeURLFetcher
// is a thin adapter, and internal/security's own tests already
// exhaustively cover SafeFetcher's real SSRF/timeout/redirect/size
// behavior, so these tests only need to prove the adapter itself:
// bytes come back on success, errors (and the options passed through)
// are propagated correctly.
type fakeURLFetcher struct {
	response *security.Response
	err      error
	gotURL   string
	gotOpts  security.FetchOptions
}

func (f *fakeURLFetcher) Fetch(ctx context.Context, rawURL string, opts security.FetchOptions) (*security.Response, error) {
	f.gotURL = rawURL
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func TestSafeURLFetcher_Fetch_Success(t *testing.T) {
	fake := &fakeURLFetcher{response: &security.Response{Body: []byte("real audio bytes")}}
	fetcher := analysisworker.NewSafeURLFetcher(fake, analysisworker.MaxAudioFetchBytes, []string{"audio/"})

	data, err := fetcher.Fetch(context.Background(), "https://example.com/audio.wav")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if string(data) != "real audio bytes" {
		t.Errorf("Fetch() = %q, want %q", data, "real audio bytes")
	}
	if fake.gotURL != "https://example.com/audio.wav" {
		t.Errorf("underlying fetcher received URL %q, want %q", fake.gotURL, "https://example.com/audio.wav")
	}
	if fake.gotOpts.MaxBytes != analysisworker.MaxAudioFetchBytes {
		t.Errorf("MaxBytes passed through = %d, want %d", fake.gotOpts.MaxBytes, analysisworker.MaxAudioFetchBytes)
	}
	if len(fake.gotOpts.AllowedContentTypes) != 1 || fake.gotOpts.AllowedContentTypes[0] != "audio/" {
		t.Errorf("AllowedContentTypes passed through = %v, want [audio/]", fake.gotOpts.AllowedContentTypes)
	}
}

func TestSafeURLFetcher_Fetch_PropagatesError(t *testing.T) {
	wantErr := &security.FetchError{Kind: security.FetchErrorBlocked, Message: "blocked by SSRF validation"}
	fake := &fakeURLFetcher{err: wantErr}
	fetcher := analysisworker.NewSafeURLFetcher(fake, analysisworker.MaxAudioFetchBytes, nil)

	_, err := fetcher.Fetch(context.Background(), "http://127.0.0.1/audio.wav")
	if !errors.Is(err, wantErr) {
		t.Errorf("Fetch() error = %v, want %v", err, wantErr)
	}
}
