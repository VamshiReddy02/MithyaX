package analysisworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
)

// Handler processes one dequeued AnalysisJob's actual work — calling
// the right Python detector and persisting the result. Worker owns
// everything else: dequeuing, timeout, retry/backoff, ack/fail,
// metrics. Splitting this out (rather than one Worker type per job
// type) is what lets a video pool and an audio pool share every bit of
// that machinery, differing only in this.
type Handler interface {
	// Handle processes job, returning an error if it failed. ctx is
	// already bounded by the caller's per-job timeout.
	Handle(ctx context.Context, job analysisjob.AnalysisJob) error
	// IsPermanent reports whether err (returned from Handle) is a
	// permanent failure — retrying it would fail identically (e.g. the
	// detector rejected malformed input) — versus a transient one worth
	// retrying (a timeout, the detector being briefly unreachable). Kept
	// on Handler rather than in Worker so Worker never needs to import
	// detector/audio to inspect their Error Kinds itself.
	IsPermanent(err error) bool
	// OnDeadLetter is called once a job is permanently given up on
	// (7.5.6) rather than retried. VideoHandler/AudioHandler use it to
	// still run the completion coordinator (7.6.6): without this, a
	// session whose audio job dead-letters while video already
	// succeeded (or vice versa) would wait forever for a risk
	// assessment that nothing will ever trigger, since only a
	// *successful* Handle normally does. Errors are logged, not
	// retried — dead-lettering itself already happened.
	OnDeadLetter(ctx context.Context, job analysisjob.AnalysisJob) error
}

// defaultRiskEngine computes the combined risk assessment stored
// alongside each modality upsert — the same internal/risk logic the
// live WebSocket pipeline uses, so a session analyzed via either path
// gets a consistently-computed verdict.
var defaultRiskEngine = risk.NewEngine()

// combineRisk adapts defaultRiskEngine to analysisrepo.ComputeRisk, so
// UpsertVideoResult/UpsertAudioResult/FinalizeRisk can recompute the
// combined assessment without internal/repository/analysis needing to
// import internal/risk itself.
func combineRisk(videoScore, audioScore, temporalScore *float64) (float64, string, []string) {
	var sig risk.Signals
	if videoScore != nil {
		sig.Video, sig.VideoOK = *videoScore, true
	}
	if audioScore != nil {
		sig.Audio, sig.AudioOK = *audioScore, true
	}
	if temporalScore != nil {
		sig.Temporal, sig.TemporalOK = *temporalScore, true
	}
	assessment := defaultRiskEngine.AssessSignals(sig)
	return assessment.RiskScore, string(assessment.Verdict), assessment.Reasons
}

// finalizeIfReady asks coordinator whether the modality that just
// finished (successfully or via dead-letter) for sessionID should
// trigger the combined risk calculation, and if so, does it —
// UpsertVideoResult/UpsertAudioResult with combineRisk when there's a
// fresh score to record alongside it, FinalizeRisk (no score to add)
// when called from OnDeadLetter. Shared by both handlers since the
// decision logic is identical either way.
func shouldComputeRisk(ctx context.Context, coordinator *Coordinator, sessionID string, modality analysisjob.Type) (analysisrepo.ComputeRisk, error) {
	ready, err := coordinator.ShouldFinalize(ctx, sessionID, modality)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil // the other modality is still outstanding — record the score, not the risk
	}
	return combineRisk, nil
}

// finalizeOnDeadLetter runs the same coordinator check for a
// permanently-failed job (which never got to upsert a score of its
// own) and, if the other modality is already terminal, computes risk
// from whatever's on record. ErrNotFound (no analysis row exists at
// all — the other modality was never requested and this one never
// succeeded either) means there's genuinely nothing to finalize, not a
// failure.
func finalizeOnDeadLetter(ctx context.Context, coordinator *Coordinator, repo analysisrepo.Repository, sessionID string, modality analysisjob.Type) error {
	ready, err := coordinator.ShouldFinalize(ctx, sessionID, modality)
	if err != nil {
		return fmt.Errorf("check completion coordinator: %w", err)
	}
	if !ready {
		return nil
	}
	if err := repo.FinalizeRisk(ctx, sessionID, combineRisk); err != nil && !errors.Is(err, analysisrepo.ErrNotFound) {
		return fmt.Errorf("finalize risk: %w", err)
	}
	return nil
}

// VideoAnalyzer analyzes a video by URL. *detector.Client implements it
// via Analyze — the existing client, reused as-is (see package doc).
type VideoAnalyzer interface {
	Analyze(ctx context.Context, videoURL string) (*detector.Result, error)
}

// VideoHandler is the Handler for VIDEO_ANALYSIS jobs (7.5.2): the
// video-detector fetches the referenced video itself (Analyze takes a
// URL, not bytes — nothing to download here), so this only calls it
// and upserts the result.
type VideoHandler struct {
	detector    VideoAnalyzer
	repo        analysisrepo.Repository
	coordinator *Coordinator
}

// NewVideoHandler builds a VideoHandler backed by detectorClient (the
// existing internal/detector.Client), repo, and coordinator (7.6.6).
func NewVideoHandler(detectorClient VideoAnalyzer, repo analysisrepo.Repository, coordinator *Coordinator) *VideoHandler {
	return &VideoHandler{detector: detectorClient, repo: repo, coordinator: coordinator}
}

func (h *VideoHandler) Handle(ctx context.Context, job analysisjob.AnalysisJob) error {
	payload, err := job.VideoPayload()
	if err != nil {
		return fmt.Errorf("decode video payload: %w", err)
	}

	result, err := h.detector.Analyze(ctx, payload.VideoURL)
	if err != nil {
		return fmt.Errorf("video-detector: %w", err)
	}

	compute, err := shouldComputeRisk(ctx, h.coordinator, job.SessionID, analysisjob.TypeVideoAnalysis)
	if err != nil {
		return fmt.Errorf("check completion coordinator: %w", err)
	}

	if err := h.repo.UpsertVideoResult(ctx, job.SessionID, result.FakeScore, result.Verdict, compute); err != nil {
		return fmt.Errorf("persist video result: %w", err)
	}
	return nil
}

func (h *VideoHandler) IsPermanent(err error) bool {
	var detErr *detector.Error
	return errors.As(err, &detErr) && detErr.Kind == detector.KindInvalidVideo
}

func (h *VideoHandler) OnDeadLetter(ctx context.Context, job analysisjob.AnalysisJob) error {
	return finalizeOnDeadLetter(ctx, h.coordinator, h.repo, job.SessionID, analysisjob.TypeVideoAnalysis)
}

// AudioFetcher fetches the bytes referenced by an AUDIO_ANALYSIS job's
// URL. A separate seam from AudioAnalyzer (below) because
// internal/audio.Client only ever accepts raw bytes (see its own doc) —
// unlike the video-detector, it has no URL-fetching mode of its own, so
// something has to fetch the reference before handing it to the
// existing client. This is deliberately not "another Python client":
// it's a plain HTTP GET.
type AudioFetcher interface {
	Fetch(ctx context.Context, audioURL string) ([]byte, error)
}

// maxAudioFetchBytes bounds a fetched audio file the same way
// /api/v1/analyze-audio bounds an uploaded one (see
// internal/handlers/analyzeaudio.go) — same class of data, arriving by
// reference instead of upload.
const maxAudioFetchBytes = 25 << 20 // 25MiB

// HTTPAudioFetcher is the real AudioFetcher: a plain, size-bounded HTTP GET.
type HTTPAudioFetcher struct {
	client *http.Client
}

// NewHTTPAudioFetcher builds an HTTPAudioFetcher using http.DefaultClient's
// transport settings but its own *http.Client, so per-call timeouts
// (via ctx) aren't shared with anything else in the process.
func NewHTTPAudioFetcher() *HTTPAudioFetcher {
	return &HTTPAudioFetcher{client: &http.Client{}}
}

func (f *HTTPAudioFetcher) Fetch(ctx context.Context, audioURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch audio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch audio: unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read audio body: %w", err)
	}
	if len(data) > maxAudioFetchBytes {
		return nil, fmt.Errorf("audio at %s exceeds %d bytes", audioURL, maxAudioFetchBytes)
	}
	return data, nil
}

// AudioAnalyzer analyzes raw audio bytes. *audio.Client implements it
// via Analyze — the existing client, reused as-is.
type AudioAnalyzer interface {
	Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error)
}

// AudioHandler is the Handler for AUDIO_ANALYSIS jobs (7.5.3): fetch
// the referenced audio, hand the bytes to the existing audio-detector
// client, upsert the result.
type AudioHandler struct {
	fetcher     AudioFetcher
	detector    AudioAnalyzer
	repo        analysisrepo.Repository
	coordinator *Coordinator
}

// NewAudioHandler builds an AudioHandler backed by fetcher, detectorClient
// (the existing internal/audio.Client), repo, and coordinator (7.6.6).
func NewAudioHandler(fetcher AudioFetcher, detectorClient AudioAnalyzer, repo analysisrepo.Repository, coordinator *Coordinator) *AudioHandler {
	return &AudioHandler{fetcher: fetcher, detector: detectorClient, repo: repo, coordinator: coordinator}
}

func (h *AudioHandler) Handle(ctx context.Context, job analysisjob.AnalysisJob) error {
	payload, err := job.AudioPayload()
	if err != nil {
		return fmt.Errorf("decode audio payload: %w", err)
	}

	data, err := h.fetcher.Fetch(ctx, payload.AudioURL)
	if err != nil {
		return fmt.Errorf("fetch audio: %w", err)
	}

	result, err := h.detector.Analyze(ctx, "chunk.wav", data)
	if err != nil {
		return fmt.Errorf("audio-detector: %w", err)
	}

	compute, err := shouldComputeRisk(ctx, h.coordinator, job.SessionID, analysisjob.TypeAudioAnalysis)
	if err != nil {
		return fmt.Errorf("check completion coordinator: %w", err)
	}

	if err := h.repo.UpsertAudioResult(ctx, job.SessionID, result.FakeScore, result.Verdict, compute); err != nil {
		return fmt.Errorf("persist audio result: %w", err)
	}
	return nil
}

func (h *AudioHandler) IsPermanent(err error) bool {
	var audErr *audio.Error
	return errors.As(err, &audErr) && audErr.Kind == audio.KindInvalidAudio
}

func (h *AudioHandler) OnDeadLetter(ctx context.Context, job analysisjob.AnalysisJob) error {
	return finalizeOnDeadLetter(ctx, h.coordinator, h.repo, job.SessionID, analysisjob.TypeAudioAnalysis)
}
