package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/session"
)

// fakeVideoAnalyzer and fakeAudioAnalyzer are controllable: an optional
// delay (to test concurrency/timeouts), a canned result or error, and a
// called flag (safe to read after Analyze() returns — the channel
// receive inside Service.Analyze synchronizes with it).
type fakeVideoAnalyzer struct {
	delay  time.Duration
	result *detector.Result
	err    error
	called bool
}

func (f *fakeVideoAnalyzer) Analyze(ctx context.Context, videoURL string) (*detector.Result, error) {
	f.called = true
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

type fakeAudioAnalyzer struct {
	delay  time.Duration
	result *audio.Result
	err    error
	called bool
}

func (f *fakeAudioAnalyzer) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	f.called = true
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func fullRequest() session.AnalyzeRequest {
	return session.AnalyzeRequest{
		VideoURL:      "https://example.com/clip.mp4",
		AudioFilename: "clip.wav",
		AudioData:     []byte("wav-bytes"),
	}
}

func TestService_BothSucceed(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.08, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.91, Verdict: "fake"}}
	svc := session.NewService(video, audioClient, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), fullRequest())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.ID == "" {
		t.Error("ID is empty")
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
	if got.Video == nil || got.Video.FakeScore != 0.08 || got.Video.Verdict != "real" {
		t.Errorf("Video = %+v, want FakeScore=0.08 Verdict=real", got.Video)
	}
	if got.Audio == nil || got.Audio.FakeScore != 0.91 || got.Audio.Verdict != "fake" {
		t.Errorf("Audio = %+v, want FakeScore=0.91 Verdict=fake", got.Audio)
	}
	if got.VideoError != "" || got.AudioError != "" {
		t.Errorf("VideoError/AudioError = %q/%q, want both empty", got.VideoError, got.AudioError)
	}
}

func TestService_VideoFailsAudioSucceeds_IsPartial(t *testing.T) {
	video := &fakeVideoAnalyzer{err: errors.New("video detector unreachable")}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.2, Verdict: "real"}}
	svc := session.NewService(video, audioClient, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), fullRequest())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.Status != session.StatusPartial {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusPartial)
	}
	if got.Video != nil {
		t.Errorf("Video = %+v, want nil", got.Video)
	}
	if got.VideoError != "video detector unreachable" {
		t.Errorf("VideoError = %q, want %q", got.VideoError, "video detector unreachable")
	}
	if got.Audio == nil || got.Audio.Verdict != "real" {
		t.Errorf("Audio = %+v, want Verdict=real (must survive video's failure)", got.Audio)
	}
}

func TestService_AudioFailsVideoSucceeds_IsPartial(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.1, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{err: errors.New("audio detector unreachable")}
	svc := session.NewService(video, audioClient, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), fullRequest())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.Status != session.StatusPartial {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusPartial)
	}
	if got.Audio != nil {
		t.Errorf("Audio = %+v, want nil", got.Audio)
	}
	if got.AudioError != "audio detector unreachable" {
		t.Errorf("AudioError = %q, want %q", got.AudioError, "audio detector unreachable")
	}
	if got.Video == nil || got.Video.Verdict != "real" {
		t.Errorf("Video = %+v, want Verdict=real (must survive audio's failure)", got.Video)
	}
}

func TestService_BothFail_IsFailed(t *testing.T) {
	video := &fakeVideoAnalyzer{err: errors.New("video boom")}
	audioClient := &fakeAudioAnalyzer{err: errors.New("audio boom")}
	svc := session.NewService(video, audioClient, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), fullRequest())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.Status != session.StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusFailed)
	}
	if got.VideoError != "video boom" || got.AudioError != "audio boom" {
		t.Errorf("VideoError/AudioError = %q/%q, want video boom/audio boom", got.VideoError, got.AudioError)
	}
}

func TestService_OnlyVideoRequested_SkipsAudio(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.1, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.5, Verdict: "real"}}
	svc := session.NewService(video, audioClient, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), session.AnalyzeRequest{VideoURL: "https://example.com/clip.mp4"})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if audioClient.called {
		t.Error("audio analyzer should not be called when no audio was submitted")
	}
	if got.Audio != nil || got.AudioError != "" {
		t.Errorf("Audio/AudioError = %+v/%q, want both empty", got.Audio, got.AudioError)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
}

func TestService_OnlyAudioRequested_SkipsVideo(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.1, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.5, Verdict: "real"}}
	svc := session.NewService(video, audioClient, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), session.AnalyzeRequest{
		AudioFilename: "clip.wav",
		AudioData:     []byte("data"),
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if video.called {
		t.Error("video analyzer should not be called when no video was submitted")
	}
	if got.Video != nil || got.VideoError != "" {
		t.Errorf("Video/VideoError = %+v/%q, want both empty", got.Video, got.VideoError)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
}

// TestService_RunsVideoAndAudioConcurrently is the core proof of the
// concurrency requirement: if the two branches ran sequentially this
// would take ~2*delay; running them concurrently keeps it near delay.
func TestService_RunsVideoAndAudioConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond
	video := &fakeVideoAnalyzer{delay: delay, result: &detector.Result{Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{delay: delay, result: &audio.Result{Verdict: "real"}}
	svc := session.NewService(video, audioClient, time.Second, time.Second)

	start := time.Now()
	_, err := svc.Analyze(context.Background(), fullRequest())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if elapsed >= 2*delay {
		t.Errorf("Analyze() took %v, want well under %v — video and audio should run concurrently, not sequentially", elapsed, 2*delay)
	}
}

// TestService_TimeoutsAreIndependent proves the two branches don't share
// a timeout budget: video is given a short timeout it blows past, while
// audio has a much longer timeout and finishes comfortably — video
// should time out without affecting audio at all.
func TestService_TimeoutsAreIndependent(t *testing.T) {
	video := &fakeVideoAnalyzer{delay: 200 * time.Millisecond, result: &detector.Result{Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{delay: 10 * time.Millisecond, result: &audio.Result{Verdict: "real"}}
	svc := session.NewService(video, audioClient, 50*time.Millisecond, time.Second)

	got, err := svc.Analyze(context.Background(), fullRequest())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.Video != nil {
		t.Errorf("Video = %+v, want nil (should have timed out)", got.Video)
	}
	if got.VideoError == "" {
		t.Error("VideoError is empty, want a deadline-exceeded error")
	}
	if got.Audio == nil || got.Audio.Verdict != "real" {
		t.Errorf("Audio = %+v, want Verdict=real — audio's own timeout is unaffected by video's", got.Audio)
	}
}

// TestService_ParentCancellationStopsBothBranchesPromptly proves both
// goroutines derive from (and respect) the caller's context, rather than
// only their own per-branch timeout.
func TestService_ParentCancellationStopsBothBranchesPromptly(t *testing.T) {
	video := &fakeVideoAnalyzer{delay: 10 * time.Second, result: &detector.Result{Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{delay: 10 * time.Second, result: &audio.Result{Verdict: "real"}}
	svc := session.NewService(video, audioClient, time.Minute, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	got, err := svc.Analyze(ctx, fullRequest())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("Analyze() took %v after parent cancellation, want it to return promptly", elapsed)
	}
	if got.Status != session.StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusFailed)
	}
	if got.VideoError == "" || got.AudioError == "" {
		t.Errorf("VideoError/AudioError = %q/%q, want both set", got.VideoError, got.AudioError)
	}
}
