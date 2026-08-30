package session_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/security"
	"github.com/vamshireddy02/mithyax/gateway/internal/session"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
)

// fakeFetcher stands in for *security.SafeFetcher — Service's video
// branch is a thin fetch-then-analyze adapter (7.8), and
// internal/security's own tests already exhaustively cover
// SafeFetcher's real SSRF/timeout/redirect/size behavior. Every test
// in this file that isn't specifically about the fetch step itself
// uses defaultFetcher, a fixed always-succeeds instance, so it never
// needs touching at each of this file's many session.NewService call
// sites.
type fakeFetcher struct {
	data []byte
	err  error
}

func (f *fakeFetcher) Fetch(ctx context.Context, rawURL string, opts security.FetchOptions) (*security.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &security.Response{Body: f.data}, nil
}

var defaultFetcher = &fakeFetcher{data: []byte("video-bytes")}

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

func (f *fakeVideoAnalyzer) AnalyzeBytes(ctx context.Context, filename string, data []byte) (*detector.Result, error) {
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

// fakeTemporalAnalyzer is controllable the same way, minus delay/err:
// TemporalAnalyzer takes no context and returns no error, since it
// never calls out to anything. gotFrames records exactly what it was
// called with, for asserting on Service's frame-source conversion.
type fakeTemporalAnalyzer struct {
	result    *temporal.TemporalResult
	called    bool
	gotFrames []temporal.Frame
}

func (f *fakeTemporalAnalyzer) Analyze(frames []temporal.Frame) *temporal.TemporalResult {
	f.called = true
	f.gotFrames = frames
	return f.result
}

func fullRequest() session.AnalyzeRequest {
	return session.AnalyzeRequest{
		VideoURL:      "https://example.com/clip.mp4",
		AudioFilename: "clip.wav",
		AudioData:     []byte("wav-bytes"),
	}
}

func someFrames() []temporal.Frame {
	return []temporal.Frame{
		{Timestamp: 0, FakeScore: 0.1, FaceDetected: true},
		{Timestamp: 1, FakeScore: 0.9, FaceDetected: true},
	}
}

// someFrameMetadata is the video-detector's-eye view of someFrames():
// the same two frames, as *detector.Client would decode them off the
// wire, including a bounding box on the detected-face entry — proving
// the conversion carries the face box through, not just the scalars.
func someFrameMetadata() []detector.FrameMetadata {
	return []detector.FrameMetadata{
		{
			Timestamp:    0,
			FakeScore:    0.1,
			FaceDetected: true,
			FaceX:        10,
			FaceY:        20,
			FaceWidth:    30,
			FaceHeight:   40,
		},
		{
			Timestamp:    1,
			FakeScore:    0.9,
			FaceDetected: false,
		},
	}
}

func TestService_BothSucceed(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.08, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.91, Verdict: "fake"}}
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

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
	if got.Temporal != nil {
		t.Errorf("Temporal = %+v, want nil (no frames were submitted)", got.Temporal)
	}
}

func TestService_VideoFailsAudioSucceeds_IsPartial(t *testing.T) {
	video := &fakeVideoAnalyzer{err: errors.New("video detector unreachable")}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.2, Verdict: "real"}}
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

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

// TestService_VideoFetchBlockedBySSRFValidation is 7.8's closure test:
// video_url now goes through URLFetcher (a real SafeFetcher in
// production) before the video-detector ever sees anything — a
// blocked fetch surfaces as VideoError, exactly like a detector
// failure already does, and the video-detector is never called at
// all.
func TestService_VideoFetchBlockedBySSRFValidation(t *testing.T) {
	blockedErr := &security.FetchError{Kind: security.FetchErrorBlocked, Message: "blocked by SSRF validation: URL resolves to a non-public address: 127.0.0.1"}
	fetcher := &fakeFetcher{err: blockedErr}
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.1, Verdict: "real"}} // should never be reached
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.2, Verdict: "real"}}
	svc := session.NewService(fetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

	req := fullRequest()
	req.VideoURL = "http://127.0.0.1:5432/"
	got, err := svc.Analyze(context.Background(), req)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.Status != session.StatusPartial {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusPartial)
	}
	if got.Video != nil {
		t.Errorf("Video = %+v, want nil", got.Video)
	}
	if got.VideoError == "" {
		t.Error("VideoError is empty, want the blocked-fetch error surfaced")
	}
	if video.called {
		t.Error("the video-detector was called despite the fetch being blocked")
	}
	if got.Audio == nil || got.Audio.Verdict != "real" {
		t.Errorf("Audio = %+v, want Verdict=real (must survive video's failure)", got.Audio)
	}
}

func TestService_AudioFailsVideoSucceeds_IsPartial(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.1, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{err: errors.New("audio detector unreachable")}
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

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
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

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
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

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
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

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
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Second, time.Second)

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
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, 50*time.Millisecond, time.Second)

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
	svc := session.NewService(defaultFetcher, video, audioClient, &fakeTemporalAnalyzer{}, time.Minute, time.Minute)

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

// TestService_TemporalNotRequested_SkipsAnalyzer proves that an empty
// Frames slice — the current reality for every real request, since
// nothing populates Frames yet — never calls the temporal analyzer at
// all, mirroring how an empty VideoURL/AudioData skips video/audio.
func TestService_TemporalNotRequested_SkipsAnalyzer(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{Verdict: "real"}}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.9}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), fullRequest())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if temporalAnalyzer.called {
		t.Error("temporal analyzer should not be called when no frames were submitted")
	}
	if got.Temporal != nil {
		t.Errorf("Temporal = %+v, want nil", got.Temporal)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
}

// TestService_TemporalRequested_IncludedInSession proves frames feed
// through to the temporal analyzer and its result lands on the session,
// adapted into session.TemporalResult.
func TestService_TemporalRequested_IncludedInSession(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.1, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.2, Verdict: "real"}}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{
		Score:           0.42,
		FramesAnalyzed:  2,
		FaceConsistency: 1,
		ScoreVariance:   0.05,
		Reasons:         []string{"stub temporal reason"},
	}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), session.AnalyzeRequest{
		VideoURL:      "https://example.com/clip.mp4",
		AudioFilename: "clip.wav",
		AudioData:     []byte("wav-bytes"),
		Frames:        someFrames(),
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if !temporalAnalyzer.called {
		t.Fatal("expected temporal analyzer to be called when frames were submitted")
	}
	if got.Temporal == nil {
		t.Fatal("Temporal = nil, want a result")
	}
	if got.Temporal.Score != 0.42 || got.Temporal.FramesAnalyzed != 2 || got.Temporal.FaceConsistency != 1 ||
		got.Temporal.ScoreVariance != 0.05 || len(got.Temporal.Reasons) != 1 || got.Temporal.Reasons[0] != "stub temporal reason" {
		t.Errorf("Temporal = %+v, want it to match the analyzer's result field for field", got.Temporal)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
}

// TestService_TemporalOnly proves temporal can carry a session on its
// own, with no video or audio requested — the session service doesn't
// require video/audio the way the HTTP handler currently does.
func TestService_TemporalOnly(t *testing.T) {
	video := &fakeVideoAnalyzer{}
	audioClient := &fakeAudioAnalyzer{}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.3, FramesAnalyzed: 2}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), session.AnalyzeRequest{Frames: someFrames()})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if video.called || audioClient.called {
		t.Error("video/audio analyzers should not be called when neither was submitted")
	}
	if got.Video != nil || got.Audio != nil {
		t.Errorf("Video/Audio = %+v/%+v, want both nil", got.Video, got.Audio)
	}
	if got.Temporal == nil || got.Temporal.Score != 0.3 {
		t.Errorf("Temporal = %+v, want Score=0.3", got.Temporal)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
}

// TestService_VideoFailsTemporalSucceeds_IsPartial proves temporal
// participates in the completed/partial/failed accounting alongside
// video/audio, even though it can never itself fail.
func TestService_VideoFailsTemporalSucceeds_IsPartial(t *testing.T) {
	video := &fakeVideoAnalyzer{err: errors.New("video detector unreachable")}
	audioClient := &fakeAudioAnalyzer{}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.1, FramesAnalyzed: 2}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), session.AnalyzeRequest{
		VideoURL: "https://example.com/clip.mp4",
		Frames:   someFrames(),
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.Status != session.StatusPartial {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusPartial)
	}
	if got.Temporal == nil || got.Temporal.Score != 0.1 {
		t.Errorf("Temporal = %+v, want Score=0.1 (must survive video's failure)", got.Temporal)
	}
	if got.VideoError != "video detector unreachable" {
		t.Errorf("VideoError = %q, want %q", got.VideoError, "video detector unreachable")
	}
}

// TestService_VideoFrameMetadata_DrivesTemporal is the core proof of
// this wiring: with no req.Frames override, video's own per-frame
// output — not some separately-submitted Frames — is what the temporal
// analyzer receives, converted field for field (including the face
// box) into []temporal.Frame.
func TestService_VideoFrameMetadata_DrivesTemporal(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{
		FakeScore:     0.1,
		Verdict:       "real",
		FrameMetadata: someFrameMetadata(),
	}}
	audioClient := &fakeAudioAnalyzer{}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.55, FramesAnalyzed: 2}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), session.AnalyzeRequest{
		VideoURL: "https://example.com/clip.mp4",
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if !temporalAnalyzer.called {
		t.Fatal("expected temporal analyzer to be called from video's frame metadata")
	}

	// The expected []temporal.Frame is someFrameMetadata() field for
	// field, including the face box on the detected entry.
	want := []temporal.Frame{
		{Timestamp: 0, FakeScore: 0.1, FaceDetected: true, FaceX: 10, FaceY: 20, FaceWidth: 30, FaceHeight: 40},
		{Timestamp: 1, FakeScore: 0.9, FaceDetected: false},
	}

	if !reflect.DeepEqual(temporalAnalyzer.gotFrames, want) {
		t.Errorf("temporal analyzer got frames = %+v, want %+v", temporalAnalyzer.gotFrames, want)
	}
	if got.Temporal == nil || got.Temporal.Score != 0.55 {
		t.Errorf("Temporal = %+v, want Score=0.55", got.Temporal)
	}

	// Frame metadata is internal plumbing only — it must never leak
	// into the session's Video field (and so never into the API
	// response), which is exactly why the sampling work on the Python
	// side would otherwise be pointless.
	if got.Video == nil || got.Video.FakeScore != 0.1 || got.Video.Verdict != "real" {
		t.Errorf("Video = %+v, want FakeScore=0.1 Verdict=real and nothing else", got.Video)
	}
}

// TestService_VideoAudioTemporal_AllThreeSucceed is the first genuine
// video + audio + temporal session: video's own frame metadata feeds
// temporal, alongside an independently-succeeding audio branch.
func TestService_VideoAudioTemporal_AllThreeSucceed(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{
		FakeScore:     0.08,
		Verdict:       "real",
		FrameMetadata: someFrameMetadata(),
	}}
	audioClient := &fakeAudioAnalyzer{result: &audio.Result{FakeScore: 0.91, Verdict: "fake"}}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.4, FramesAnalyzed: 2}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), fullRequest())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
	if got.Video == nil || got.Video.Verdict != "real" {
		t.Errorf("Video = %+v, want Verdict=real", got.Video)
	}
	if got.Audio == nil || got.Audio.Verdict != "fake" {
		t.Errorf("Audio = %+v, want Verdict=fake", got.Audio)
	}
	if got.Temporal == nil || got.Temporal.Score != 0.4 {
		t.Errorf("Temporal = %+v, want Score=0.4", got.Temporal)
	}
	if len(temporalAnalyzer.gotFrames) != 2 {
		t.Errorf("temporal analyzer got %d frames, want 2", len(temporalAnalyzer.gotFrames))
	}
}

// TestService_VideoSucceeds_NoFrameMetadata_SkipsTemporal covers the
// "missing frame metadata" case: video succeeds but returns no
// per-frame data (e.g. an older/mocked video-detector response).
// Temporal must be skipped gracefully, with everything else about the
// session — the existing video-only behavior — unaffected.
func TestService_VideoSucceeds_NoFrameMetadata_SkipsTemporal(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{FakeScore: 0.2, Verdict: "real"}}
	audioClient := &fakeAudioAnalyzer{}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.9}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	got, err := svc.Analyze(context.Background(), session.AnalyzeRequest{
		VideoURL: "https://example.com/clip.mp4",
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if temporalAnalyzer.called {
		t.Error("temporal analyzer should not be called when video returned no frame metadata")
	}
	if got.Temporal != nil {
		t.Errorf("Temporal = %+v, want nil", got.Temporal)
	}
	if got.Video == nil || got.Video.FakeScore != 0.2 || got.Video.Verdict != "real" {
		t.Errorf("Video = %+v, want FakeScore=0.2 Verdict=real — unaffected by the missing frame metadata", got.Video)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, session.StatusCompleted)
	}
}

// TestService_ReqFramesOverridesVideoFrameMetadata proves an explicit
// req.Frames still wins over video's own frame metadata when both are
// present, per framesForTemporal's documented precedence.
func TestService_ReqFramesOverridesVideoFrameMetadata(t *testing.T) {
	video := &fakeVideoAnalyzer{result: &detector.Result{
		FakeScore:     0.1,
		Verdict:       "real",
		FrameMetadata: someFrameMetadata(),
	}}
	audioClient := &fakeAudioAnalyzer{}
	temporalAnalyzer := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.2}}
	svc := session.NewService(defaultFetcher, video, audioClient, temporalAnalyzer, time.Second, time.Second)

	override := []temporal.Frame{{Timestamp: 0, FakeScore: 0.5, FaceDetected: true}}

	_, err := svc.Analyze(context.Background(), session.AnalyzeRequest{
		VideoURL: "https://example.com/clip.mp4",
		Frames:   override,
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if !reflect.DeepEqual(temporalAnalyzer.gotFrames, override) {
		t.Errorf("temporal analyzer got frames = %+v, want the req.Frames override %+v", temporalAnalyzer.gotFrames, override)
	}
}
