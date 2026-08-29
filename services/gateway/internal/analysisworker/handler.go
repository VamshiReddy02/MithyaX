package analysisworker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"

	"github.com/vamshireddy02/mithyax/gateway/internal/analysisjob"
	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
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

// URLFetcher fetches the bytes referenced by an AnalysisJob's URL
// payload (video_url or audio_url) — used by both VideoHandler and
// AudioHandler (7.7.5): as of this phase both modalities follow the
// identical shape "SSRF-safe fetch, then hand bytes to the Python
// detector." SafeURLFetcher is the real implementation.
type URLFetcher interface {
	Fetch(ctx context.Context, rawURL string) ([]byte, error)
}

// MaxVideoFetchBytes and MaxAudioFetchBytes bound how much a
// SafeURLFetcher will download for each modality — same class of limit
// /api/v1/analyze-audio already applies to an uploaded file (see
// internal/handlers/analyzeaudio.go), applied here to one fetched by
// reference instead. Exported for httpserver.New to wire into
// NewSafeURLFetcher. "Eventually configuration rather than hardcoded
// values" per 7.7.5 — not yet needed until real usage says these
// defaults are wrong.
const (
	MaxVideoFetchBytes = 100 << 20 // 100MiB
	MaxAudioFetchBytes = 25 << 20  // 25MiB
)

// urlFetcher is the subset of *security.SafeFetcher's API SafeURLFetcher
// actually needs, narrowed to an interface — the same reason
// VideoAnalyzer/AudioAnalyzer below are interfaces rather than concrete
// clients — so tests can substitute a fake instead of needing a real
// network fetch; internal/security's own tests already exhaustively
// cover *security.SafeFetcher's real behavior.
type urlFetcher interface {
	Fetch(ctx context.Context, rawURL string, opts security.FetchOptions) (*security.Response, error)
}

// SafeURLFetcher is the real URLFetcher (7.7.5): every fetch goes
// through security.SafeFetcher, which validates the URL (a fresh DNS
// lookup, not just whatever the API checked when the job was created —
// DNS can change while a job sits in Redis), pins the connection to
// the address it just validated, follows redirects only after
// validating each target the same way, and bounds both response size
// and Content-Type.
type SafeURLFetcher struct {
	fetcher      urlFetcher
	maxBytes     int64
	contentTypes []string
}

// NewSafeURLFetcher builds a SafeURLFetcher backed by fetcher — in
// production, always a real *security.SafeFetcher, which satisfies
// urlFetcher structurally — bounding each fetch to maxBytes and, if
// allowedContentTypes is non-empty, rejecting any other Content-Type
// (see security.FetchOptions).
func NewSafeURLFetcher(fetcher urlFetcher, maxBytes int64, allowedContentTypes []string) *SafeURLFetcher {
	return &SafeURLFetcher{fetcher: fetcher, maxBytes: maxBytes, contentTypes: allowedContentTypes}
}

func (f *SafeURLFetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	resp, err := f.fetcher.Fetch(ctx, rawURL, security.FetchOptions{MaxBytes: f.maxBytes, AllowedContentTypes: f.contentTypes})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// isFetchPermanent classifies a URLFetcher failure the way both
// Handler implementations need — blocked by SSRF validation, too many
// redirects, response too large, wrong content type, or an
// unacceptable (4xx vs. 5xx) status already know their own permanence;
// see FetchError.IsPermanent. A non-FetchError (e.g. a payload decode
// failure that happened before any fetch was attempted) isn't this
// function's concern — callers check that separately.
func isFetchPermanent(err error) bool {
	var fetchErr *security.FetchError
	return errors.As(err, &fetchErr) && fetchErr.IsPermanent()
}

// VideoAnalyzer analyzes raw video bytes. *detector.Client implements
// it via AnalyzeBytes — the existing client, reused as-is.
type VideoAnalyzer interface {
	AnalyzeBytes(ctx context.Context, filename string, data []byte) (*detector.Result, error)
}

// VideoHandler is the Handler for VIDEO_ANALYSIS jobs (7.5.2): fetch
// the referenced video through a SafeURLFetcher (7.7.5 — SSRF-safe,
// size-bounded, redirect-validated, DNS-rebinding-safe; see
// internal/security's package doc), hand the bytes to the
// video-detector, upsert the result.
//
// Before 7.7.5 this handed video_url straight to the video-detector,
// which fetched it in its own process — completely outside this
// package's SSRF protection. Fetching the bytes here instead, exactly
// like AudioHandler already did, closes that gap: the video-detector
// now never makes an outbound request to a client-supplied URL at all
// (see its /analyze-upload endpoint).
type VideoHandler struct {
	fetcher     URLFetcher
	detector    VideoAnalyzer
	repo        analysisrepo.Repository
	coordinator *Coordinator
}

// NewVideoHandler builds a VideoHandler backed by fetcher, detectorClient
// (the existing internal/detector.Client), repo, and coordinator (7.6.6).
func NewVideoHandler(fetcher URLFetcher, detectorClient VideoAnalyzer, repo analysisrepo.Repository, coordinator *Coordinator) *VideoHandler {
	return &VideoHandler{fetcher: fetcher, detector: detectorClient, repo: repo, coordinator: coordinator}
}

func (h *VideoHandler) Handle(ctx context.Context, job analysisjob.AnalysisJob) error {
	payload, err := job.VideoPayload()
	if err != nil {
		return fmt.Errorf("decode video payload: %w", err)
	}

	data, err := h.fetcher.Fetch(ctx, payload.VideoURL)
	if err != nil {
		return fmt.Errorf("fetch video: %w", err)
	}

	result, err := h.detector.AnalyzeBytes(ctx, filenameFromURL(payload.VideoURL), data)
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

// filenameFromURL extracts a file name (with extension) from a URL's
// path, the same way the video-detector's own /analyze endpoint infers
// one from video_url — preserved here so /analyze-upload sees the same
// container-format hint (via the file extension) it always would have
// from the URL itself, e.g. clip.mov rather than an assumed clip.mp4.
func filenameFromURL(rawURL string) string {
	const fallback = "video.mp4"
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fallback
	}
	name := path.Base(parsed.Path)
	if name == "" || name == "." || name == "/" {
		return fallback
	}
	return name
}

func (h *VideoHandler) IsPermanent(err error) bool {
	var detErr *detector.Error
	if errors.As(err, &detErr) && detErr.Kind == detector.KindInvalidVideo {
		return true
	}
	return isFetchPermanent(err)
}

func (h *VideoHandler) OnDeadLetter(ctx context.Context, job analysisjob.AnalysisJob) error {
	return finalizeOnDeadLetter(ctx, h.coordinator, h.repo, job.SessionID, analysisjob.TypeVideoAnalysis)
}

// AudioAnalyzer analyzes raw audio bytes. *audio.Client implements it
// via Analyze — the existing client, reused as-is.
type AudioAnalyzer interface {
	Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error)
}

// AudioHandler is the Handler for AUDIO_ANALYSIS jobs (7.5.3): fetch
// the referenced audio through a SafeURLFetcher (7.7.5), hand the
// bytes to the existing audio-detector client, upsert the result.
type AudioHandler struct {
	fetcher     URLFetcher
	detector    AudioAnalyzer
	repo        analysisrepo.Repository
	coordinator *Coordinator
}

// NewAudioHandler builds an AudioHandler backed by fetcher, detectorClient
// (the existing internal/audio.Client), repo, and coordinator (7.6.6).
func NewAudioHandler(fetcher URLFetcher, detectorClient AudioAnalyzer, repo analysisrepo.Repository, coordinator *Coordinator) *AudioHandler {
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
	if errors.As(err, &audErr) && audErr.Kind == audio.KindInvalidAudio {
		return true
	}
	return isFetchPermanent(err)
}

func (h *AudioHandler) OnDeadLetter(ctx context.Context, job analysisjob.AnalysisJob) error {
	return finalizeOnDeadLetter(ctx, h.coordinator, h.repo, job.SessionID, analysisjob.TypeAudioAnalysis)
}
