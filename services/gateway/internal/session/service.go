// Package session orchestrates a combined video + audio + temporal
// analysis: it runs video and audio against their respective Python
// detector services concurrently, runs temporal locally against
// whatever frame metadata was supplied, and combines all three results
// into a single AnalysisSession — the risk engine's source of truth.
package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"path"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
)

// VideoAnalyzer performs a whole-video analysis on bytes Service
// already fetched itself (see URLFetcher). detector.Client implements
// it via AnalyzeBytes.
type VideoAnalyzer interface {
	AnalyzeBytes(ctx context.Context, filename string, data []byte) (*detector.Result, error)
}

// URLFetcher fetches the bytes referenced by a video_url before
// Service ever hands them to a detector — see security.SafeFetcher,
// the real implementation. Phase 7.8 closed a gap left open since
// 7.7.5: video_url used to go straight to the video-detector
// (Analyze(ctx, videoURL)), which fetched it in its own process,
// entirely outside SafeFetcher's SSRF/redirect/size/timeout
// protections. Fetching the bytes here instead — exactly like
// internal/analysisworker.VideoHandler and internal/worker.Pool
// already do — closes it for this pipeline too.
type URLFetcher interface {
	Fetch(ctx context.Context, rawURL string, opts security.FetchOptions) (*security.Response, error)
}

// maxVideoFetchBytes bounds a fetched video file — same limit
// internal/analysisworker.MaxVideoFetchBytes and internal/worker's own
// identical constant use. Duplicated rather than imported across these
// independent packages, matching this codebase's established
// convention (see internal/worker.Pool's own copy of this same
// constant and helper).
const maxVideoFetchBytes = 100 << 20 // 100MiB

// AudioAnalyzer performs a whole-file audio analysis. audio.Client
// implements it.
type AudioAnalyzer interface {
	Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error)
}

// AnalyzeRequest is one session's input. At least one of VideoURL or
// AudioData should be set — Service only runs the branches with input.
//
// Frames is an explicit override for the temporal branch's input,
// taking priority over whatever frame metadata video analysis itself
// returns (see framesForTemporal) — useful for exercising temporal
// without a real video call. In the normal flow nothing needs to set
// it: once video succeeds, Service derives temporal's frames from the
// video-detector's own per-frame output automatically.
type AnalyzeRequest struct {
	VideoURL      string
	AudioFilename string
	AudioData     []byte
	Frames        []temporal.Frame
}

// Service coordinates a session's video, audio, and temporal analyses.
// Video and audio each run in their own goroutine, on their own
// timeout, derived from the same parent context — so a slow video
// analysis never delays audio (or vice versa), and cancelling ctx (e.g.
// the caller disconnecting) stops both. Temporal runs synchronously on
// the calling goroutine, after both of those complete: unlike
// video/audio it's a local, in-memory computation with no I/O to wait
// on, so it needs neither a timeout nor a goroutine of its own — but it
// does need to wait, since its usual source of frames is video's own
// per-frame output (see framesForTemporal).
type Service struct {
	videoFetcher     URLFetcher
	videoClient      VideoAnalyzer
	audioClient      AudioAnalyzer
	temporalAnalyzer TemporalAnalyzer
	videoTimeout     time.Duration
	audioTimeout     time.Duration
}

// NewService builds a Service. videoFetcher is, in production, always
// a real *security.SafeFetcher — see URLFetcher's own doc for why
// video_url goes through it before videoClient ever sees any bytes.
// videoTimeout and audioTimeout bound each remote branch independently
// — video analysis is typically much slower than audio, so sharing one
// timeout would either starve audio's budget or let a hanging video
// call run far longer than audio ever should. Temporal analysis has no
// timeout since it never calls out.
func NewService(videoFetcher URLFetcher, videoClient VideoAnalyzer, audioClient AudioAnalyzer, temporalAnalyzer TemporalAnalyzer, videoTimeout, audioTimeout time.Duration) *Service {
	return &Service{
		videoFetcher:     videoFetcher,
		videoClient:      videoClient,
		audioClient:      audioClient,
		temporalAnalyzer: temporalAnalyzer,
		videoTimeout:     videoTimeout,
		audioTimeout:     audioTimeout,
	}
}

type videoOutcome struct {
	result *VideoResult
	err    error
	// frameMetadata is the video-detector's raw per-frame output —
	// kept out of VideoResult (and so out of AnalysisSession.Video and
	// the API response) since it exists only to feed the temporal
	// branch, not to be exposed to callers.
	frameMetadata []detector.FrameMetadata
}

type audioOutcome struct {
	result *AudioResult
	err    error
}

// Analyze runs the requested video, audio, and temporal analyses and
// returns their combined result.
//
// It only returns a non-nil error if a session ID couldn't be generated
// — a failure in the video or audio branch is instead reported on the
// returned AnalysisSession (VideoError/AudioError), so a successful
// audio result isn't discarded just because video failed, or vice
// versa. That's also why video/audio use plain goroutines and channels
// rather than errgroup.WithContext: WithContext cancels every other
// goroutine the instant one returns an error, which would throw away a
// perfectly good result from the branch that didn't fail.
func (s *Service) Analyze(ctx context.Context, req AnalyzeRequest) (*AnalysisSession, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to create session id: %w", err)
	}

	videoCh := make(chan videoOutcome, 1)
	audioCh := make(chan audioOutcome, 1)

	go func() {
		if req.VideoURL == "" {
			videoCh <- videoOutcome{}
			return
		}
		videoCtx, cancel := context.WithTimeout(ctx, s.videoTimeout)
		defer cancel()
		videoCh <- s.runVideo(videoCtx, req.VideoURL)
	}()

	go func() {
		if len(req.AudioData) == 0 {
			audioCh <- audioOutcome{}
			return
		}
		audioCtx, cancel := context.WithTimeout(ctx, s.audioTimeout)
		defer cancel()
		audioCh <- s.runAudio(audioCtx, req.AudioFilename, req.AudioData)
	}()

	// Receiving from both channels blocks until each goroutine has sent
	// — this is the synchronization point (equivalent to a WaitGroup),
	// and it's what makes reading each outcome afterward race-free.
	video := <-videoCh
	audio := <-audioCh

	// Runs on this goroutine, not its own, after both of the above:
	// it's a local computation with no I/O to overlap, and its usual
	// frame source is video's own output, which isn't available any
	// earlier than this.
	temporalResult := s.runTemporal(framesForTemporal(req, video))

	return buildSession(id, video, audio, temporalResult), nil
}

func (s *Service) runVideo(ctx context.Context, videoURL string) videoOutcome {
	resp, err := s.videoFetcher.Fetch(ctx, videoURL, security.FetchOptions{
		MaxBytes:            maxVideoFetchBytes,
		AllowedContentTypes: []string{"video/"},
	})
	if err != nil {
		return videoOutcome{err: err}
	}

	result, err := s.videoClient.AnalyzeBytes(ctx, filenameFromURL(videoURL), resp.Body)
	if err != nil {
		return videoOutcome{err: err}
	}
	return videoOutcome{
		result:        &VideoResult{FakeScore: result.FakeScore, Verdict: result.Verdict},
		frameMetadata: result.FrameMetadata,
	}
}

// filenameFromURL extracts a file name (with extension) from a URL's
// path — the video-detector uses it to infer the container format, the
// same way its /analyze endpoint always could from video_url directly.
// Duplicated from internal/worker's and internal/analysisworker's
// identical helper (see this package's own doc on URLFetcher for why
// importing across these independent packages isn't done just for
// this).
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

func (s *Service) runAudio(ctx context.Context, filename string, data []byte) audioOutcome {
	result, err := s.audioClient.Analyze(ctx, filename, data)
	if err != nil {
		return audioOutcome{err: err}
	}
	return audioOutcome{result: &AudioResult{FakeScore: result.FakeScore, Verdict: result.Verdict}}
}

func buildSession(id string, video videoOutcome, audio audioOutcome, temporalResult *TemporalResult) *AnalysisSession {
	session := &AnalysisSession{ID: id, Temporal: temporalResult}

	videoRequested := video.result != nil || video.err != nil
	audioRequested := audio.result != nil || audio.err != nil
	temporalRequested := temporalResult != nil

	if video.result != nil {
		session.Video = video.result
	}
	if video.err != nil {
		session.VideoError = video.err.Error()
	}
	if audio.result != nil {
		session.Audio = audio.result
	}
	if audio.err != nil {
		session.AudioError = audio.err.Error()
	}

	var succeeded, failed int
	if videoRequested {
		if video.err != nil {
			failed++
		} else {
			succeeded++
		}
	}
	if audioRequested {
		if audio.err != nil {
			failed++
		} else {
			succeeded++
		}
	}
	if temporalRequested {
		// Temporal never fails — it's a local computation, not a call to
		// an external service — so a requested temporal branch always
		// counts as a success.
		succeeded++
	}

	switch {
	case failed == 0:
		session.Status = StatusCompleted
	case succeeded == 0:
		session.Status = StatusFailed
	default:
		session.Status = StatusPartial
	}

	return session
}

// newSessionID generates a random UUID v4 string.
func newSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
