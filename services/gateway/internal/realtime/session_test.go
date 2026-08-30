package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/realtime"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
)

// smallCfg is a small, fast Config for tests that don't care about the
// specific queue/worker sizes.
var smallCfg = realtime.Config{MaxVideoQueue: 5, VideoWorkers: 2, MaxAudioQueue: 5, AudioWorkers: 2, MaxSessions: 10}

// fakeVideoFrameAnalyzer, fakeAudioChunkAnalyzer, fakeTemporalAnalyzer,
// and fakeRiskEngine are simple canned-result/error fakes for tests that
// don't need to control call timing.
type fakeVideoFrameAnalyzer struct {
	mu      sync.Mutex
	results []*detector.FrameResult // consumed in order, one per call
	err     error
	calls   int
}

func (f *fakeVideoFrameAnalyzer) AnalyzeFrame(ctx context.Context, jpeg []byte) (*detector.FrameResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.results[(f.calls-1)%len(f.results)], nil
}

type fakeAudioChunkAnalyzer struct {
	mu     sync.Mutex
	result *audio.Result
	err    error
	calls  int
}

func (f *fakeAudioChunkAnalyzer) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

type fakeTemporalAnalyzer struct {
	mu        sync.Mutex
	result    *temporal.TemporalResult
	gotFrames []temporal.Frame
}

func (f *fakeTemporalAnalyzer) Analyze(frames []temporal.Frame) *temporal.TemporalResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotFrames = frames
	return f.result
}

type fakeRiskEngine struct {
	mu         sync.Mutex
	gotSignals []risk.Signals // one entry per call, in order
	result     risk.Assessment
}

func (f *fakeRiskEngine) AssessSignals(sig risk.Signals) risk.Assessment {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotSignals = append(f.gotSignals, sig)
	return f.result
}

func (f *fakeRiskEngine) signals() []risk.Signals {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]risk.Signals, len(f.gotSignals))
	copy(out, f.gotSignals)
	return out
}

// controllableVideoAnalyzer and controllableAudioAnalyzer give
// fine-grained control over call timing — blocking until released,
// per-frame delays, and per-frame content keyed to a delay — which the
// concurrency tests below need to force frames into a specific queue
// state deterministically rather than racing against goroutine
// scheduling.
type controllableVideoAnalyzer struct {
	mu          sync.Mutex
	received    [][]byte
	block       chan struct{}            // if set, every call waits for this to close
	callStarted chan struct{}            // if set, signaled (best-effort) when a call begins
	delays      map[string]time.Duration // per-content artificial delay
	result      *detector.FrameResult
}

func (f *controllableVideoAnalyzer) AnalyzeFrame(ctx context.Context, jpeg []byte) (*detector.FrameResult, error) {
	f.mu.Lock()
	f.received = append(f.received, jpeg)
	delay := f.delays[string(jpeg)]
	f.mu.Unlock()

	if f.callStarted != nil {
		select {
		case f.callStarted <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if f.result != nil {
		return f.result, nil
	}
	return &detector.FrameResult{FaceDetected: true, FakeProbability: 0.1, Verdict: "real"}, nil
}

func (f *controllableVideoAnalyzer) receivedFrames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.received))
	copy(out, f.received)
	return out
}

type controllableAudioAnalyzer struct {
	mu          sync.Mutex
	received    [][]byte
	block       chan struct{}
	callStarted chan struct{}
	result      *audio.Result
}

func (f *controllableAudioAnalyzer) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	f.mu.Lock()
	f.received = append(f.received, data)
	f.mu.Unlock()

	if f.callStarted != nil {
		select {
		case f.callStarted <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if f.result != nil {
		return f.result, nil
	}
	return &audio.Result{FakeScore: 0.1, Verdict: "real"}, nil
}

func (f *controllableAudioAnalyzer) receivedChunks() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.received))
	copy(out, f.received)
	return out
}

func newTestSession(t *testing.T, video realtime.VideoFrameAnalyzer, aud realtime.AudioChunkAnalyzer, temp realtime.TemporalAnalyzer, riskEngine realtime.RiskAssessor) *realtime.Session {
	t.Helper()
	store := realtime.NewStore(video, aud, temp, riskEngine, smallCfg)
	session, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return session
}

// drainMessages reads exactly want messages off out, or fails the test
// if that many don't arrive within timeout or the channel closes early.
func drainMessages(t *testing.T, out <-chan realtime.OutMessage, want int, timeout time.Duration) []realtime.OutMessage {
	t.Helper()

	got := make([]realtime.OutMessage, 0, want)
	deadline := time.After(timeout)
	for len(got) < want {
		select {
		case msg, ok := <-out:
			if !ok {
				t.Fatalf("Out() closed early: got %d/%d messages: %+v", len(got), want, got)
			}
			got = append(got, msg)
		case <-deadline:
			t.Fatalf("timed out waiting for %d messages, got %d: %+v", want, len(got), got)
		}
	}
	return got
}

func submitFrameAndDrain(t *testing.T, session *realtime.Session, jpeg []byte, want int) []realtime.OutMessage {
	t.Helper()
	if !session.SubmitFrame(jpeg) {
		t.Fatal("SubmitFrame() = false, want true")
	}
	return drainMessages(t, session.Out(), want, 2*time.Second)
}

func submitAudioAndDrain(t *testing.T, session *realtime.Session, filename string, data []byte, want int) []realtime.OutMessage {
	t.Helper()
	if !session.SubmitAudioChunk(filename, data) {
		t.Fatal("SubmitAudioChunk() = false, want true")
	}
	return drainMessages(t, session.Out(), want, 2*time.Second)
}

func TestSession_Create_StartsActive(t *testing.T) {
	session := newTestSession(t, &fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{})
	defer session.End()

	if session.ID() == "" {
		t.Error("ID is empty")
	}
	if session.Status() != realtime.StatusActive {
		t.Errorf("Status() = %q, want %q", session.Status(), realtime.StatusActive)
	}
}

func TestSession_Deadline_NoLimitConfigured_ReturnsZero(t *testing.T) {
	session := newTestSession(t, &fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{})
	defer session.End()

	if got := session.Deadline(); !got.IsZero() {
		t.Errorf("Deadline() = %v, want the zero time (smallCfg sets no MaxSessionDuration)", got)
	}
}

func TestSession_Deadline_WithLimitConfigured(t *testing.T) {
	cfg := realtime.Config{MaxVideoQueue: 5, VideoWorkers: 1, MaxAudioQueue: 5, AudioWorkers: 1, MaxSessions: 10, MaxSessionDuration: time.Hour}
	store := realtime.NewStore(&fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, cfg)

	before := time.Now()
	session, err := store.Create()
	after := time.Now()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer session.End()

	deadline := session.Deadline()

	if deadline.IsZero() {
		t.Fatal("Deadline() = zero time, want approximately one hour from now")
	}
	if deadline.Before(before.Add(time.Hour)) || deadline.After(after.Add(time.Hour)) {
		t.Errorf("Deadline() = %v, want within [%v, %v]", deadline, before.Add(time.Hour), after.Add(time.Hour))
	}
}

func TestSession_SubmitFrame_ProducesVideoResultThenRiskUpdate(t *testing.T) {
	video := &fakeVideoFrameAnalyzer{results: []*detector.FrameResult{
		{FaceDetected: true, FakeProbability: 0.8, Verdict: "fake"},
	}}
	temp := &fakeTemporalAnalyzer{result: nil} // not enough frames yet
	riskEngine := &fakeRiskEngine{result: risk.Assessment{RiskScore: 0.8, Verdict: risk.VerdictLikelyFake}}
	session := newTestSession(t, video, &fakeAudioChunkAnalyzer{}, temp, riskEngine)
	defer session.End()

	messages := submitFrameAndDrain(t, session, []byte("jpeg-bytes"), 2)

	video0 := messages[0]
	if video0.Type != realtime.TypeVideoResult || video0.FakeScore == nil || *video0.FakeScore != 0.8 ||
		video0.FaceDetected == nil || !*video0.FaceDetected || video0.Verdict != "fake" {
		t.Errorf("messages[0] = %+v, want video_result FakeScore=0.8 FaceDetected=true Verdict=fake", video0)
	}

	risk0 := messages[1]
	if risk0.Type != realtime.TypeRiskUpdate || risk0.RiskScore == nil || *risk0.RiskScore != 0.8 {
		t.Errorf("messages[1] = %+v, want risk_update RiskScore=0.8", risk0)
	}

	signals := riskEngine.signals()
	if len(signals) != 1 {
		t.Fatalf("risk engine called %d times, want 1", len(signals))
	}
	if !signals[0].VideoOK || signals[0].Video != 0.8 {
		t.Errorf("Signals = %+v, want VideoOK=true Video=0.8", signals[0])
	}
	if signals[0].AudioOK || signals[0].TemporalOK {
		t.Errorf("Signals = %+v, want AudioOK/TemporalOK both false (neither has arrived yet)", signals[0])
	}
}

func TestSession_SubmitFrame_NoFaceDetected_ExcludedFromRiskSignal(t *testing.T) {
	video := &fakeVideoFrameAnalyzer{results: []*detector.FrameResult{
		{FaceDetected: false, FakeProbability: 0.0, Verdict: "unknown"},
	}}
	riskEngine := &fakeRiskEngine{}
	session := newTestSession(t, video, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, riskEngine)
	defer session.End()

	submitFrameAndDrain(t, session, []byte("jpeg-bytes"), 2)

	signals := riskEngine.signals()
	if signals[0].VideoOK {
		t.Error("Signals.VideoOK = true, want false (no face detected)")
	}
}

// TestSession_SubmitFrame_TemporalInsufficientData_ExcludedFromRiskSignal
// reproduces a bug found during real end-to-end testing: on the very
// first frame, temporal.Analyzer.Analyze returns a non-nil result with
// FramesAnalyzed=1 and a zero-value Score, purely to explain "not
// enough data yet" — it isn't a real "looks authentic" measurement. Left
// unguarded, a session's first risk_update falsely reported
// LIKELY_AUTHENTIC instead of reflecting that nothing is known yet.
func TestSession_SubmitFrame_TemporalInsufficientData_ExcludedFromRiskSignal(t *testing.T) {
	video := &fakeVideoFrameAnalyzer{results: []*detector.FrameResult{
		{FaceDetected: false, FakeProbability: 0.0, Verdict: "unknown"},
	}}
	temp := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{
		FramesAnalyzed: 1,
		Score:          0,
		Reasons:        []string{"insufficient temporal data: at least 2 frames are required"},
	}}
	riskEngine := &fakeRiskEngine{}
	session := newTestSession(t, video, &fakeAudioChunkAnalyzer{}, temp, riskEngine)
	defer session.End()

	messages := submitFrameAndDrain(t, session, []byte("jpeg-bytes"), 3)

	if messages[1].Type != realtime.TypeTemporalResult {
		t.Fatalf("messages = %+v, want [video_result, temporal_result, risk_update]", messages)
	}

	signals := riskEngine.signals()
	if signals[0].TemporalOK {
		t.Error("Signals.TemporalOK = true, want false (temporal hasn't seen enough frames yet)")
	}
}

func TestSession_SubmitFrame_DetectorError_EmitsErrorOnly(t *testing.T) {
	video := &fakeVideoFrameAnalyzer{err: errors.New("video detector unreachable")}
	session := newTestSession(t, video, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{})
	defer session.End()

	messages := submitFrameAndDrain(t, session, []byte("jpeg-bytes"), 1)

	if messages[0].Type != realtime.TypeError || messages[0].Message == "" {
		t.Errorf("messages[0] = %+v, want a non-empty error message", messages[0])
	}
}

func TestSession_SubmitFrame_EnoughFrames_IncludesTemporalResult(t *testing.T) {
	video := &fakeVideoFrameAnalyzer{results: []*detector.FrameResult{
		{FaceDetected: true, FakeProbability: 0.1, Verdict: "real"},
	}}
	temp := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{
		Score: 0.42, FramesAnalyzed: 2, FaceConsistency: 1, ScoreVariance: 0.01, Reasons: []string{"stub"},
	}}
	riskEngine := &fakeRiskEngine{result: risk.Assessment{RiskScore: 0.3, Verdict: risk.VerdictSuspicious}}
	session := newTestSession(t, video, &fakeAudioChunkAnalyzer{}, temp, riskEngine)
	defer session.End()

	messages := submitFrameAndDrain(t, session, []byte("jpeg-bytes"), 3)

	if messages[1].Type != realtime.TypeTemporalResult || messages[1].Score == nil || *messages[1].Score != 0.42 {
		t.Errorf("messages[1] = %+v, want temporal_result Score=0.42", messages[1])
	}
	if messages[2].Type != realtime.TypeRiskUpdate {
		t.Errorf("messages[2].Type = %q, want risk_update", messages[2].Type)
	}

	signals := riskEngine.signals()
	if !signals[0].TemporalOK || signals[0].Temporal != 0.42 {
		t.Errorf("Signals = %+v, want TemporalOK=true Temporal=0.42", signals[0])
	}
}

func TestSession_SubmitAudioChunk_ProducesAudioResultThenRiskUpdate(t *testing.T) {
	aud := &fakeAudioChunkAnalyzer{result: &audio.Result{FakeScore: 0.91, Verdict: "fake"}}
	riskEngine := &fakeRiskEngine{result: risk.Assessment{RiskScore: 0.91, Verdict: risk.VerdictLikelyFake}}
	session := newTestSession(t, &fakeVideoFrameAnalyzer{}, aud, &fakeTemporalAnalyzer{}, riskEngine)
	defer session.End()

	messages := submitAudioAndDrain(t, session, "chunk.wav", []byte("audio-bytes"), 2)

	if messages[0].Type != realtime.TypeAudioResult || messages[0].FakeScore == nil || *messages[0].FakeScore != 0.91 {
		t.Errorf("messages[0] = %+v, want audio_result FakeScore=0.91", messages[0])
	}

	signals := riskEngine.signals()
	if !signals[0].AudioOK || signals[0].Audio != 0.91 {
		t.Errorf("Signals = %+v, want AudioOK=true Audio=0.91", signals[0])
	}
}

func TestSession_SubmitAudioChunk_DetectorError_EmitsErrorOnly(t *testing.T) {
	aud := &fakeAudioChunkAnalyzer{err: errors.New("audio detector unreachable")}
	session := newTestSession(t, &fakeVideoFrameAnalyzer{}, aud, &fakeTemporalAnalyzer{}, &fakeRiskEngine{})
	defer session.End()

	messages := submitAudioAndDrain(t, session, "chunk.wav", []byte("audio-bytes"), 1)

	if messages[0].Type != realtime.TypeError || messages[0].Message == "" {
		t.Errorf("messages[0] = %+v, want a non-empty error message", messages[0])
	}
}

// TestSession_VideoAudioTemporal_AllThreeFeedTheSameRiskUpdate proves
// signals accumulate across calls: video from one frame, audio from one
// chunk, and temporal from a second frame are all present by the time
// the third risk_update is computed — a live session's whole point.
func TestSession_VideoAudioTemporal_AllThreeFeedTheSameRiskUpdate(t *testing.T) {
	video := &fakeVideoFrameAnalyzer{results: []*detector.FrameResult{
		{FaceDetected: true, FakeProbability: 0.2, Verdict: "real"},
		{FaceDetected: true, FakeProbability: 0.3, Verdict: "real"},
	}}
	aud := &fakeAudioChunkAnalyzer{result: &audio.Result{FakeScore: 0.9, Verdict: "fake"}}
	temp := &fakeTemporalAnalyzer{result: &temporal.TemporalResult{Score: 0.4, FramesAnalyzed: 2}}
	riskEngine := &fakeRiskEngine{result: risk.Assessment{RiskScore: 0.5, Verdict: risk.VerdictSuspicious}}
	session := newTestSession(t, video, aud, temp, riskEngine)
	defer session.End()

	submitFrameAndDrain(t, session, []byte("frame-1"), 3) // video_result, temporal_result, risk_update
	submitAudioAndDrain(t, session, "chunk.wav", []byte("audio"), 2)
	submitFrameAndDrain(t, session, []byte("frame-2"), 3)

	signals := riskEngine.signals()
	final := signals[len(signals)-1]
	if !final.VideoOK || final.Video != 0.3 {
		t.Errorf("final Signals.Video = %+v, want VideoOK=true Video=0.3 (latest frame)", final)
	}
	if !final.AudioOK || final.Audio != 0.9 {
		t.Errorf("final Signals.Audio = %+v, want AudioOK=true Audio=0.9", final)
	}
	if !final.TemporalOK || final.Temporal != 0.4 {
		t.Errorf("final Signals.Temporal = %+v, want TemporalOK=true Temporal=0.4", final)
	}

	if len(temp.gotFrames) != 2 {
		t.Errorf("temporal analyzer got %d frames, want 2 (both processed frames)", len(temp.gotFrames))
	}
}

func TestSession_End_SetsStatusEndedAndClosesOut(t *testing.T) {
	session := newTestSession(t, &fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{})

	msg := session.End()

	if msg.Type != realtime.TypeSessionEnded || msg.ID != session.ID() {
		t.Errorf("End() = %+v, want session_ended for %s", msg, session.ID())
	}
	if session.Status() != realtime.StatusEnded {
		t.Errorf("Status() = %q, want %q", session.Status(), realtime.StatusEnded)
	}

	// Out() must be closed once End() returns — no worker can still be
	// mid-send after End()'s wg.Wait().
	select {
	case _, ok := <-session.Out():
		if ok {
			t.Error("Out() yielded a message after End(), want it closed with nothing buffered")
		}
	default:
		t.Error("Out() did not appear closed immediately after End()")
	}
}

func TestSession_End_CalledTwice_Idempotent(t *testing.T) {
	session := newTestSession(t, &fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{})

	first := session.End()
	second := session.End()

	if first.Type != second.Type || first.ID != second.ID {
		t.Errorf("End() calls returned different messages: %+v vs %+v", first, second)
	}
}

func TestStore_GetAfterCreate_Found(t *testing.T) {
	store := realtime.NewStore(&fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, smallCfg)

	created, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer created.End()

	got, ok := store.Get(created.ID())
	if !ok || got != created {
		t.Errorf("Get(%q) = %v, %v, want the created session", created.ID(), got, ok)
	}
}

func TestStore_GetUnknownID_NotFound(t *testing.T) {
	store := realtime.NewStore(&fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, smallCfg)

	if _, ok := store.Get("does-not-exist"); ok {
		t.Error("Get() found a session for an unknown ID")
	}
}

func TestStore_Delete_RemovesSession(t *testing.T) {
	store := realtime.NewStore(&fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, smallCfg)

	created, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer created.End()

	store.Delete(created.ID())

	if _, ok := store.Get(created.ID()); ok {
		t.Error("Get() found a session after Delete()")
	}
}

// TestStore_MaxSessions_RejectsBeyondLimit proves the configured session
// cap is actually enforced, and that it tracks active count rather than
// permanently exhausting after the limit is first hit.
func TestStore_MaxSessions_RejectsBeyondLimit(t *testing.T) {
	cfg := realtime.Config{MaxVideoQueue: 5, VideoWorkers: 1, MaxAudioQueue: 5, AudioWorkers: 1, MaxSessions: 2}
	store := realtime.NewStore(&fakeVideoFrameAnalyzer{}, &fakeAudioChunkAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, cfg)

	first, err := store.Create()
	if err != nil {
		t.Fatalf("Create() #1 error = %v", err)
	}
	defer first.End()

	second, err := store.Create()
	if err != nil {
		t.Fatalf("Create() #2 error = %v", err)
	}
	defer second.End()

	if _, err := store.Create(); !errors.Is(err, realtime.ErrTooManySessions) {
		t.Fatalf("Create() #3 error = %v, want ErrTooManySessions", err)
	}

	// Ending and removing one session frees a slot.
	first.End()
	store.Delete(first.ID())

	third, err := store.Create()
	if err != nil {
		t.Fatalf("Create() after freeing a slot: error = %v, want success", err)
	}
	defer third.End()
}

// TestSession_VideoQueueFull_DropsOldestKeepsNewest is the core proof
// of the backpressure policy for video: with the sole worker busy on
// frame-1, three more frames arrive against a 2-slot queue. The middle
// one (frame-2) must be dropped to make room for the newest (frame-4) —
// exactly "discard stale frames, process newest".
func TestSession_VideoQueueFull_DropsOldestKeepsNewest(t *testing.T) {
	video := &controllableVideoAnalyzer{block: make(chan struct{}), callStarted: make(chan struct{}, 1)}
	cfg := realtime.Config{MaxVideoQueue: 2, VideoWorkers: 1, MaxAudioQueue: 5, AudioWorkers: 1, MaxSessions: 10}
	store := realtime.NewStore(video, &controllableAudioAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, cfg)
	session, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer session.End()

	if !session.SubmitFrame([]byte("frame-1")) {
		t.Fatal("SubmitFrame(frame-1) = false, want true")
	}
	<-video.callStarted // the sole worker is now blocked inside AnalyzeFrame(frame-1)

	if !session.SubmitFrame([]byte("frame-2")) {
		t.Fatal("SubmitFrame(frame-2) = false, want true (queue has room)")
	}
	if !session.SubmitFrame([]byte("frame-3")) {
		t.Fatal("SubmitFrame(frame-3) = false, want true (queue now full but still accepts)")
	}
	// Queue (cap 2) holds [frame-2, frame-3] and is now full: frame-4
	// must still be accepted by dropping frame-2, not rejected.
	if !session.SubmitFrame([]byte("frame-4")) {
		t.Fatal("SubmitFrame(frame-4) = false, want true (video drops oldest rather than rejecting)")
	}

	close(video.block) // let frame-1 finish, then the worker drains [frame-3, frame-4]

	// Drain until frame-1, frame-3, and frame-4 have all been processed
	// (3 * 2 messages each: video_result + risk_update, no temporal
	// since the fake temporal analyzer returns nil).
	drainMessages(t, session.Out(), 6, 2*time.Second)

	got := video.receivedFrames()
	want := [][]byte{[]byte("frame-1"), []byte("frame-3"), []byte("frame-4")}
	if len(got) != len(want) {
		t.Fatalf("video analyzer received %d frames %v, want %v", len(got), asStrings(got), asStrings(want))
	}
	for i := range want {
		if string(got[i]) != string(want[i]) {
			t.Errorf("received[%d] = %q, want %q — frame-2 should have been dropped, not frame-3", i, got[i], want[i])
		}
	}

	snap := store.Metrics()
	if snap.FramesReceived != 4 {
		t.Errorf("FramesReceived = %d, want 4", snap.FramesReceived)
	}
	if snap.FramesDropped != 1 {
		t.Errorf("FramesDropped = %d, want 1 (only frame-2)", snap.FramesDropped)
	}
	if snap.FramesProcessed != 3 {
		t.Errorf("FramesProcessed = %d, want 3", snap.FramesProcessed)
	}
}

func asStrings(bs [][]byte) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = string(b)
	}
	return out
}

// TestSession_AudioQueueFull_RejectsRatherThanDropping proves audio's
// different overload policy: unlike video, a full audio queue rejects
// the newest chunk (SubmitAudioChunk returns false) instead of dropping
// an older, already-queued one — silently discarding part of a speech
// stream would create a gap in the analysis.
func TestSession_AudioQueueFull_RejectsRatherThanDropping(t *testing.T) {
	aud := &controllableAudioAnalyzer{block: make(chan struct{}), callStarted: make(chan struct{}, 1)}
	cfg := realtime.Config{MaxVideoQueue: 5, VideoWorkers: 1, MaxAudioQueue: 1, AudioWorkers: 1, MaxSessions: 10}
	store := realtime.NewStore(&controllableVideoAnalyzer{}, aud, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, cfg)
	session, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer session.End()

	if !session.SubmitAudioChunk("chunk-1.wav", []byte("chunk-1")) {
		t.Fatal("SubmitAudioChunk(chunk-1) = false, want true")
	}
	<-aud.callStarted // the sole worker is now blocked inside Analyze(chunk-1)

	if !session.SubmitAudioChunk("chunk-2.wav", []byte("chunk-2")) {
		t.Fatal("SubmitAudioChunk(chunk-2) = false, want true (queue has its one slot free)")
	}
	// Queue (cap 1) now holds chunk-2 and is full.
	if session.SubmitAudioChunk("chunk-3.wav", []byte("chunk-3")) {
		t.Error("SubmitAudioChunk(chunk-3) = true, want false — a full audio queue must reject, not silently drop")
	}

	close(aud.block)

	drainMessages(t, session.Out(), 4, 2*time.Second) // chunk-1 and chunk-2: audio_result + risk_update each

	snap := store.Metrics()
	if snap.AudioChunksReceived != 3 {
		t.Errorf("AudioChunksReceived = %d, want 3", snap.AudioChunksReceived)
	}
	if snap.AudioChunksDropped != 1 {
		t.Errorf("AudioChunksDropped = %d, want 1 (chunk-3, the rejected one)", snap.AudioChunksDropped)
	}
	if snap.AudioChunksProcessed != 2 {
		t.Errorf("AudioChunksProcessed = %d, want 2", snap.AudioChunksProcessed)
	}

	got := aud.receivedChunks()
	if len(got) != 2 || string(got[0]) != "chunk-1" || string(got[1]) != "chunk-2" {
		t.Errorf("received = %v, want [chunk-1 chunk-2] — chunk-3 must never reach the audio-detector", asStrings(got))
	}
}

// TestSession_HundredFramesRapid_BoundedAndAccountedFor is the "100
// incoming frames" backpressure test: memory is bounded by
// construction (the queue is a fixed-capacity channel), so this proves
// the pipeline doesn't lose track of any frame under a burst — every
// one is either processed or counted as dropped, never both or
// neither.
func TestSession_HundredFramesRapid_BoundedAndAccountedFor(t *testing.T) {
	video := &controllableVideoAnalyzer{}
	cfg := realtime.Config{MaxVideoQueue: 5, VideoWorkers: 2, MaxAudioQueue: 5, AudioWorkers: 1, MaxSessions: 10}
	store := realtime.NewStore(video, &controllableAudioAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, cfg)
	session, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Drain Out() concurrently so workers never block trying to emit
	// results — the point of this test is the queue/drop accounting,
	// not message content.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range session.Out() {
		}
	}()

	const n = 100
	for i := 0; i < n; i++ {
		session.SubmitFrame([]byte(fmt.Sprintf("frame-%d", i)))
	}

	deadline := time.After(5 * time.Second)
waitLoop:
	for {
		snap := store.Metrics()
		if snap.FramesProcessed+snap.FramesDropped >= n {
			break waitLoop
		}
		select {
		case <-deadline:
			t.Fatalf("timed out: processed=%d dropped=%d, want sum=%d", snap.FramesProcessed, snap.FramesDropped, n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	session.End()
	<-drainDone

	snap := store.Metrics()
	if snap.FramesReceived != n {
		t.Errorf("FramesReceived = %d, want %d", snap.FramesReceived, n)
	}
	if snap.FramesProcessed+snap.FramesDropped != n {
		t.Errorf("FramesProcessed(%d)+FramesDropped(%d) = %d, want %d — every submitted frame must be accounted for exactly once",
			snap.FramesProcessed, snap.FramesDropped, snap.FramesProcessed+snap.FramesDropped, n)
	}
	if snap.FramesDropped == 0 {
		t.Error("FramesDropped = 0, want some drops: 100 frames arrived instantly against a 5-slot queue with 2 workers")
	}
}

// TestSession_SlowVideoDetector_AudioContinues proves video and audio
// run on independent queues/workers: a video call that's artificially
// slow must not delay the audio_result for a chunk submitted at the
// same time.
func TestSession_SlowVideoDetector_AudioContinues(t *testing.T) {
	video := &controllableVideoAnalyzer{delays: map[string]time.Duration{"slow-frame": 500 * time.Millisecond}}
	aud := &controllableAudioAnalyzer{result: &audio.Result{FakeScore: 0.2, Verdict: "real"}}
	cfg := realtime.Config{MaxVideoQueue: 5, VideoWorkers: 1, MaxAudioQueue: 5, AudioWorkers: 1, MaxSessions: 10}
	store := realtime.NewStore(video, aud, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, cfg)
	session, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer session.End()

	if !session.SubmitFrame([]byte("slow-frame")) {
		t.Fatal("SubmitFrame() = false, want true")
	}

	start := time.Now()
	messages := submitAudioAndDrain(t, session, "chunk.wav", []byte("audio-1"), 2)
	elapsed := time.Since(start)

	if messages[0].Type != realtime.TypeAudioResult {
		t.Fatalf("messages[0].Type = %q, want audio_result", messages[0].Type)
	}
	if elapsed >= 400*time.Millisecond {
		t.Errorf("audio_result took %v, want well under the video call's 500ms delay — audio must not be blocked behind video", elapsed)
	}
}

// TestSession_MultipleSessions_OneDoesNotStarveAnother proves each
// Session's workers are independent: a slow frame in session A must not
// delay session B's own frame, even though both sessions share the same
// Store (and so the same underlying detector clients).
func TestSession_MultipleSessions_OneDoesNotStarveAnother(t *testing.T) {
	video := &controllableVideoAnalyzer{delays: map[string]time.Duration{"a-frame-1": 500 * time.Millisecond}}
	cfg := realtime.Config{MaxVideoQueue: 5, VideoWorkers: 1, MaxAudioQueue: 5, AudioWorkers: 1, MaxSessions: 10}
	store := realtime.NewStore(video, &controllableAudioAnalyzer{}, &fakeTemporalAnalyzer{}, &fakeRiskEngine{}, cfg)

	sessionA, err := store.Create()
	if err != nil {
		t.Fatalf("Create() A error = %v", err)
	}
	defer sessionA.End()

	sessionB, err := store.Create()
	if err != nil {
		t.Fatalf("Create() B error = %v", err)
	}
	defer sessionB.End()

	if !sessionA.SubmitFrame([]byte("a-frame-1")) {
		t.Fatal("SubmitFrame(A) = false, want true")
	}

	start := time.Now()
	messages := submitFrameAndDrain(t, sessionB, []byte("b-frame-1"), 2)
	elapsed := time.Since(start)

	if messages[0].Type != realtime.TypeVideoResult {
		t.Fatalf("messages[0].Type = %q, want video_result", messages[0].Type)
	}
	if elapsed >= 400*time.Millisecond {
		t.Errorf("session B took %v to process its frame while session A was busy, want well under A's 500ms delay", elapsed)
	}
}
