// Package session orchestrates a combined video + audio analysis: it
// runs both against their respective Python detector services
// concurrently and combines the results, instead of waiting for one to
// finish before starting the other.
package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
)

// VideoAnalyzer performs a whole-video analysis. detector.Client
// implements it.
type VideoAnalyzer interface {
	Analyze(ctx context.Context, videoURL string) (*detector.Result, error)
}

// AudioAnalyzer performs a whole-file audio analysis. audio.Client
// implements it.
type AudioAnalyzer interface {
	Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error)
}

// AnalyzeRequest is one session's input. At least one of VideoURL or
// AudioData should be set — Service only runs the branches with input.
type AnalyzeRequest struct {
	VideoURL      string
	AudioFilename string
	AudioData     []byte
}

// Service coordinates a session's video and audio analyses. Each branch
// runs in its own goroutine, on its own timeout, derived from the same
// parent context — so a slow video analysis never delays audio (or vice
// versa), and cancelling ctx (e.g. the caller disconnecting) stops both.
type Service struct {
	videoClient  VideoAnalyzer
	audioClient  AudioAnalyzer
	videoTimeout time.Duration
	audioTimeout time.Duration
}

// NewService builds a Service. videoTimeout and audioTimeout bound each
// branch independently — video analysis is typically much slower than
// audio, so sharing one timeout would either starve audio's budget or
// let a hanging video call run far longer than audio ever should.
func NewService(videoClient VideoAnalyzer, audioClient AudioAnalyzer, videoTimeout, audioTimeout time.Duration) *Service {
	return &Service{
		videoClient:  videoClient,
		audioClient:  audioClient,
		videoTimeout: videoTimeout,
		audioTimeout: audioTimeout,
	}
}

type videoOutcome struct {
	result *VideoResult
	err    error
}

type audioOutcome struct {
	result *AudioResult
	err    error
}

// Analyze runs the requested video and/or audio analyses concurrently
// and returns their combined result.
//
// It only returns a non-nil error if a session ID couldn't be generated
// — a failure in either branch is instead reported on the returned
// AnalysisSession (VideoError/AudioError), so a successful audio result
// isn't discarded just because video failed, or vice versa. That's also
// why this uses plain goroutines and channels rather than
// errgroup.WithContext: WithContext cancels every other goroutine the
// instant one returns an error, which would throw away a perfectly good
// result from the branch that didn't fail.
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

	return buildSession(id, video, audio), nil
}

func (s *Service) runVideo(ctx context.Context, videoURL string) videoOutcome {
	result, err := s.videoClient.Analyze(ctx, videoURL)
	if err != nil {
		return videoOutcome{err: err}
	}
	return videoOutcome{result: &VideoResult{FakeScore: result.FakeScore, Verdict: result.Verdict}}
}

func (s *Service) runAudio(ctx context.Context, filename string, data []byte) audioOutcome {
	result, err := s.audioClient.Analyze(ctx, filename, data)
	if err != nil {
		return audioOutcome{err: err}
	}
	return audioOutcome{result: &AudioResult{FakeScore: result.FakeScore, Verdict: result.Verdict}}
}

func buildSession(id string, video videoOutcome, audio audioOutcome) *AnalysisSession {
	session := &AnalysisSession{ID: id}

	videoRequested := video.result != nil || video.err != nil
	audioRequested := audio.result != nil || audio.err != nil

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
