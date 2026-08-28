// Package realtime implements MithyaX's live analysis session: a
// browser streams video frames and audio chunks over a WebSocket
// instead of uploading a finished file, and gets video/audio/temporal/
// risk results back as they're computed. It reuses the same detector
// clients, temporal analyzer, and risk engine the uploaded-video
// pipeline (internal/session, internal/risk) already uses — a live
// session just feeds them incrementally, one frame or chunk at a time,
// instead of all at once.
//
// Each Session runs its own bounded video and audio queues, drained by
// a small pool of worker goroutines per queue (see Config) — a browser
// sampling frames faster than the video-detector can keep up doesn't
// pile up unbounded work or block audio behind it. See SubmitFrame and
// SubmitAudioChunk for the two queues' different overload policies.
package realtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
)

// Status describes a live session's lifecycle state.
type Status string

const (
	// StatusActive means the session accepts frame/audio_chunk messages.
	StatusActive Status = "active"
	// StatusEnded means the session's WebSocket has closed (or the
	// browser sent end_session) — it no longer processes messages.
	StatusEnded Status = "ended"
)

// maxFrameBuffer bounds how many recent frames the temporal analyzer
// sees. A live session runs indefinitely, unlike the uploaded-video
// pipeline (which downsamples one known, finite frame set) — so this
// keeps a rolling window of only the most recent frames rather than
// growing without bound for the life of a long call.
const maxFrameBuffer = 60

// outBuffer sizes each Session's outbound message channel. A single
// frame can produce up to three messages (video_result, temporal_result,
// risk_update), so this gives workers room to stay ahead of a
// momentarily slow WritePump without blocking on it.
const outBuffer = 64

// VideoFrameAnalyzer analyzes a single JPEG frame. *detector.Client
// implements it via AnalyzeFrame.
type VideoFrameAnalyzer interface {
	AnalyzeFrame(ctx context.Context, jpeg []byte) (*detector.FrameResult, error)
}

// AudioChunkAnalyzer analyzes one chunk of raw audio bytes.
// *audio.Client implements it via Analyze.
type AudioChunkAnalyzer interface {
	Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error)
}

// TemporalAnalyzer computes a temporal analysis from accumulated
// frames. *temporal.Analyzer implements it.
type TemporalAnalyzer interface {
	Analyze(frames []temporal.Frame) *temporal.TemporalResult
}

// RiskAssessor computes a risk assessment from whichever signals are
// available. *risk.Engine implements it via AssessSignals.
type RiskAssessor interface {
	AssessSignals(sig risk.Signals) risk.Assessment
}

type frameJob struct {
	data []byte
}

type audioJob struct {
	filename string
	data     []byte
}

// Session is one live analysis session: it accumulates video, audio,
// and temporal signals as they arrive and recomputes a risk assessment
// after every update. A Session has no idea it's attached to a
// WebSocket — see Client for that — so its message-producing methods
// can be tested directly, the same way session.Service.Analyze is
// tested independently of the HTTP handler that calls it.
type Session struct {
	id        string
	createdAt time.Time

	videoClient      VideoFrameAnalyzer
	audioClient      AudioChunkAnalyzer
	temporalAnalyzer TemporalAnalyzer
	riskEngine       RiskAssessor
	metrics          *Metrics

	videoQueue chan frameJob
	audioQueue chan audioJob
	out        chan OutMessage

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu     sync.Mutex
	status Status
	frames []temporal.Frame

	videoScore    *float64
	audioScore    *float64
	temporalScore *float64
}

func newSession(id string, videoClient VideoFrameAnalyzer, audioClient AudioChunkAnalyzer, temporalAnalyzer TemporalAnalyzer, riskEngine RiskAssessor, cfg Config, metrics *Metrics) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	s := &Session{
		id:               id,
		createdAt:        time.Now(),
		status:           StatusActive,
		videoClient:      videoClient,
		audioClient:      audioClient,
		temporalAnalyzer: temporalAnalyzer,
		riskEngine:       riskEngine,
		metrics:          metrics,
		videoQueue:       make(chan frameJob, cfg.MaxVideoQueue),
		audioQueue:       make(chan audioJob, cfg.MaxAudioQueue),
		out:              make(chan OutMessage, outBuffer),
		ctx:              ctx,
		cancel:           cancel,
	}

	for i := 0; i < cfg.VideoWorkers; i++ {
		s.wg.Add(1)
		go s.videoWorker()
	}
	for i := 0; i < cfg.AudioWorkers; i++ {
		s.wg.Add(1)
		go s.audioWorker()
	}

	return s
}

// ID returns this session's ID.
func (s *Session) ID() string { return s.id }

// Status returns this session's current lifecycle state.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Out returns the channel Session publishes every OutMessage on —
// video_result, audio_result, temporal_result, risk_update, and error.
// It's closed once End has finished shutting down every worker, after
// which no further sends occur.
func (s *Session) Out() <-chan OutMessage {
	return s.out
}

// SubmitFrame enqueues jpeg for a video worker to analyze. If the video
// queue is already full, the oldest queued frame is dropped to make
// room for this one — for real-time video, freshness matters more than
// processing every frame in order, and letting the queue grow instead
// would only add latency that never recovers. Returns false in the rare
// case the frame couldn't be enqueued even after dropping the oldest
// one; the caller should tell the browser the session is overloaded.
func (s *Session) SubmitFrame(jpeg []byte) bool {
	s.metrics.framesReceived.Add(1)
	job := frameJob{data: jpeg}

	select {
	case s.videoQueue <- job:
		s.metrics.videoQueueDepth.Add(1)
		return true
	default:
	}

	// Full: drop the oldest queued frame to make room for the newest.
	select {
	case <-s.videoQueue:
		s.metrics.videoQueueDepth.Add(-1)
		s.metrics.framesDropped.Add(1)
	default:
		// A worker drained the last item between the two selects above —
		// nothing to drop; fall through and just try to enqueue.
	}

	select {
	case s.videoQueue <- job:
		s.metrics.videoQueueDepth.Add(1)
		return true
	default:
		// Vanishingly unlikely: the queue filled again in the instant
		// between the drop and this send. Drop the incoming frame rather
		// than spin or block the caller.
		s.metrics.framesDropped.Add(1)
		return false
	}
}

// SubmitAudioChunk enqueues one audio chunk for an audio worker to
// analyze. Unlike video, a full audio queue does not drop anything to
// make room: silently discarding part of a speech stream can create
// gaps that distort the analysis, so instead the new chunk is rejected
// outright and the caller is expected to surface that to the browser as
// an explicit overload signal (see ErrCodeOverloaded).
func (s *Session) SubmitAudioChunk(filename string, data []byte) bool {
	s.metrics.audioChunksReceived.Add(1)

	select {
	case s.audioQueue <- audioJob{filename: filename, data: data}:
		s.metrics.audioQueueDepth.Add(1)
		return true
	default:
		s.metrics.audioChunksDropped.Add(1)
		return false
	}
}

func (s *Session) videoWorker() {
	defer s.wg.Done()
	for {
		select {
		case job := <-s.videoQueue:
			s.metrics.videoQueueDepth.Add(-1)
			s.processFrame(job.data)
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Session) audioWorker() {
	defer s.wg.Done()
	for {
		select {
		case job := <-s.audioQueue:
			s.metrics.audioQueueDepth.Add(-1)
			s.processAudioChunk(job.filename, job.data)
		case <-s.ctx.Done():
			return
		}
	}
}

// processFrame analyzes one JPEG frame and folds it into the session's
// rolling state, emitting a video_result for the frame itself, a
// temporal_result once enough frames have accumulated to say anything,
// and always a final risk_update reflecting every signal gathered so
// far. Runs on a video worker goroutine.
func (s *Session) processFrame(jpeg []byte) {
	start := time.Now()
	result, err := s.videoClient.AnalyzeFrame(s.ctx, jpeg)
	s.metrics.videoLatency.observe(time.Since(start))

	if err != nil {
		if s.ctx.Err() != nil {
			return // session ended mid-call; nothing left to report to
		}
		s.emit(OutMessage{Type: TypeError, Message: err.Error()})
		return
	}
	s.metrics.framesProcessed.Add(1)

	s.mu.Lock()
	messages := []OutMessage{videoResultMessage(result)}

	// A frame with no face detected has no fake-probability worth
	// trusting (the video-detector returns 0.0 as a placeholder, not a
	// measurement) — excluded from the video signal entirely, same as
	// risk.weightedScore excludes any missing signal rather than
	// treating it as a confident 0.
	if result.FaceDetected {
		score := result.FakeProbability
		s.videoScore = &score
	}

	s.frames = append(s.frames, temporal.Frame{
		Timestamp:    time.Since(s.createdAt).Seconds(),
		FakeScore:    result.FakeProbability,
		FaceDetected: result.FaceDetected,
	})
	if len(s.frames) > maxFrameBuffer {
		s.frames = s.frames[len(s.frames)-maxFrameBuffer:]
	}

	if temporalResult := s.temporalAnalyzer.Analyze(s.frames); temporalResult != nil {
		// FramesAnalyzed < 2 means temporal.Analyzer didn't have enough
		// frames to compute anything yet — Score is a zero-value
		// placeholder, not a real "looks authentic" measurement (see
		// temporal.Analyzer.Analyze's single-frame case). Sending it to
		// the risk engine anyway would make a session's very first
		// risk_update falsely report LIKELY_AUTHENTIC instead of
		// reflecting that nothing is known yet. Still send the message
		// itself, so the browser can show that temporal is warming up.
		if temporalResult.FramesAnalyzed >= 2 {
			score := temporalResult.Score
			s.temporalScore = &score
		}
		messages = append(messages, temporalResultMessage(temporalResult))
	}

	messages = append(messages, s.riskUpdateLocked())
	s.mu.Unlock()

	for _, msg := range messages {
		s.emit(msg)
	}
}

// processAudioChunk analyzes one chunk of raw audio and folds it into
// the session's rolling state, emitting an audio_result followed by a
// risk_update — the same shape as processFrame. Runs on an audio worker
// goroutine.
func (s *Session) processAudioChunk(filename string, data []byte) {
	start := time.Now()
	result, err := s.audioClient.Analyze(s.ctx, filename, data)
	s.metrics.audioLatency.observe(time.Since(start))

	if err != nil {
		if s.ctx.Err() != nil {
			return
		}
		s.emit(OutMessage{Type: TypeError, Message: err.Error()})
		return
	}
	s.metrics.audioChunksProcessed.Add(1)

	s.mu.Lock()
	score := result.FakeScore
	s.audioScore = &score

	messages := []OutMessage{audioResultMessage(result), s.riskUpdateLocked()}
	s.mu.Unlock()

	for _, msg := range messages {
		s.emit(msg)
	}
}

// emit publishes msg on Out(), unless the session has already ended —
// once ctx is canceled, nothing should still be waiting to send, since
// End() doesn't close Out() until every worker (the only senders) has
// exited.
func (s *Session) emit(msg OutMessage) {
	select {
	case s.out <- msg:
	case <-s.ctx.Done():
	}
}

// End marks the session as no longer active, stops every worker
// (canceling any detector call currently in flight), and returns the
// session_ended message for the caller to send if the connection is
// still open. Safe to call more than once — only the first call does
// anything; the rest just return the same message.
//
// End blocks until every worker goroutine has actually exited, which in
// the worst case is bounded by however long the video/audio clients'
// own HTTP timeout allows an in-flight call to run before ctx
// cancellation aborts it — not by the caller.
func (s *Session) End() OutMessage {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.status = StatusEnded
		s.mu.Unlock()

		s.cancel()
		s.wg.Wait()
		close(s.out)
	})
	return OutMessage{Type: TypeSessionEnded, ID: s.id}
}

// signalsLocked builds risk.Signals from whichever scores have been
// gathered so far. Callers must hold s.mu.
func (s *Session) signalsLocked() risk.Signals {
	var sig risk.Signals

	if s.videoScore != nil {
		sig.Video, sig.VideoOK = *s.videoScore, true
	}
	if s.audioScore != nil {
		sig.Audio, sig.AudioOK = *s.audioScore, true
	}
	if s.temporalScore != nil {
		sig.Temporal, sig.TemporalOK = *s.temporalScore, true
	}

	return sig
}

// riskUpdateLocked builds the risk_update message from whichever
// signals have been gathered so far. Callers must hold s.mu.
func (s *Session) riskUpdateLocked() OutMessage {
	return riskUpdateMessage(s.riskEngine.AssessSignals(s.signalsLocked()))
}

// FinalAssessment recomputes the risk assessment from whatever signals
// this session ever gathered. Safe to call after End(): the underlying
// scores are plain fields set only by worker goroutines, all of which
// have exited by the time End() returns. Used to persist a session's
// final risk_score/verdict once it ends (see internal/repository/sessions).
func (s *Session) FinalAssessment() risk.Assessment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.riskEngine.AssessSignals(s.signalsLocked())
}

func videoResultMessage(r *detector.FrameResult) OutMessage {
	fakeScore := r.FakeProbability
	faceDetected := r.FaceDetected
	return OutMessage{
		Type:         TypeVideoResult,
		FakeScore:    &fakeScore,
		FaceDetected: &faceDetected,
		Verdict:      r.Verdict,
	}
}

func audioResultMessage(r *audio.Result) OutMessage {
	fakeScore := r.FakeScore
	return OutMessage{Type: TypeAudioResult, FakeScore: &fakeScore, Verdict: r.Verdict}
}

func temporalResultMessage(r *temporal.TemporalResult) OutMessage {
	score := r.Score
	framesAnalyzed := r.FramesAnalyzed
	faceConsistency := r.FaceConsistency
	scoreVariance := r.ScoreVariance

	return OutMessage{
		Type:            TypeTemporalResult,
		Score:           &score,
		FramesAnalyzed:  &framesAnalyzed,
		FaceConsistency: &faceConsistency,
		ScoreVariance:   &scoreVariance,
		Reasons:         r.Reasons,
	}
}

func riskUpdateMessage(a risk.Assessment) OutMessage {
	riskScore := a.RiskScore
	return OutMessage{
		Type:      TypeRiskUpdate,
		RiskScore: &riskScore,
		Verdict:   string(a.Verdict),
		Reasons:   a.Reasons,
	}
}

// ErrTooManySessions is returned by Store.Create when MaxSessions
// active sessions are already running.
var ErrTooManySessions = errors.New("too many active sessions")

// Store creates and looks up live Sessions, keyed by ID. It's in-memory
// only (like websocket.Hub's rooms) — a live session is inherently tied
// to a single WebSocket connection to this server instance, so there's
// nothing to gain from persisting it anywhere more durable.
type Store struct {
	videoClient      VideoFrameAnalyzer
	audioClient      AudioChunkAnalyzer
	temporalAnalyzer TemporalAnalyzer
	riskEngine       RiskAssessor
	cfg              Config
	metrics          *Metrics

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewStore builds a Store whose Sessions all share the given analyzers
// and Config.
func NewStore(videoClient VideoFrameAnalyzer, audioClient AudioChunkAnalyzer, temporalAnalyzer TemporalAnalyzer, riskEngine RiskAssessor, cfg Config) *Store {
	return &Store{
		videoClient:      videoClient,
		audioClient:      audioClient,
		temporalAnalyzer: temporalAnalyzer,
		riskEngine:       riskEngine,
		cfg:              cfg,
		metrics:          newMetrics(),
		sessions:         make(map[string]*Session),
	}
}

// Create builds a new active Session and registers it in the store.
// Returns ErrTooManySessions if cfg.MaxSessions active sessions already
// exist.
func (st *Store) Create() (*Session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to create session id: %w", err)
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.sessions) >= st.cfg.MaxSessions {
		return nil, ErrTooManySessions
	}

	session := newSession(id, st.videoClient, st.audioClient, st.temporalAnalyzer, st.riskEngine, st.cfg, st.metrics)
	st.sessions[id] = session
	return session, nil
}

// Get looks up a session by ID.
func (st *Store) Get(id string) (*Session, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	session, ok := st.sessions[id]
	return session, ok
}

// Delete removes a session from the store. Called once its WebSocket
// connection closes, so a live session's memory doesn't outlive the
// connection it belongs to.
func (st *Store) Delete(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, id)
}

// Metrics returns a point-in-time snapshot of every session's combined
// counters and gauges.
func (st *Store) Metrics() MetricsSnapshot {
	return st.metrics.Snapshot()
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
