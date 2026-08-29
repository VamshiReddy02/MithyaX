package handlers_test

import (
	"context"
	"sync"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
	analysisrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/analysis"
	sessionrepo "github.com/vamshireddy02/mithyax/gateway/internal/repository/sessions"
	"github.com/vamshireddy02/mithyax/gateway/internal/risk"
	"github.com/vamshireddy02/mithyax/gateway/internal/temporal"
)

// fakeRealtimeVideoAnalyzer, fakeRealtimeAudioAnalyzer,
// fakeRealtimeTemporalAnalyzer, and fakeRealtimeRiskEngine satisfy
// realtime.Store's four analyzer interfaces, so handler-level tests can
// build a real *realtime.Store without needing live detector services.
type fakeRealtimeVideoAnalyzer struct {
	result *detector.FrameResult
}

func (f *fakeRealtimeVideoAnalyzer) AnalyzeFrame(ctx context.Context, jpeg []byte) (*detector.FrameResult, error) {
	if f.result != nil {
		return f.result, nil
	}
	return &detector.FrameResult{FaceDetected: true, FakeProbability: 0.1, Verdict: "real"}, nil
}

type fakeRealtimeAudioAnalyzer struct {
	result *audio.Result
}

func (f *fakeRealtimeAudioAnalyzer) Analyze(ctx context.Context, filename string, data []byte) (*audio.Result, error) {
	if f.result != nil {
		return f.result, nil
	}
	return &audio.Result{FakeScore: 0.1, Verdict: "real"}, nil
}

type fakeRealtimeTemporalAnalyzer struct{}

func (f *fakeRealtimeTemporalAnalyzer) Analyze(frames []temporal.Frame) *temporal.TemporalResult {
	return nil
}

type fakeRealtimeRiskEngine struct{}

// AssessSignals returns a fixed score/verdict (this fake isn't testing
// the risk engine's own weighting logic — see internal/risk for that)
// but still passes each present signal through to Assessment.Signals,
// the same way the real engine does. Tests that check the persisted
// per-modality breakdown (see analysisResult in
// TestSessionWebSocket_FullLifecycle) depend on that pass-through
// actually happening here, not just in production.
func (f *fakeRealtimeRiskEngine) AssessSignals(sig risk.Signals) risk.Assessment {
	assessment := risk.Assessment{RiskScore: 0.1, Verdict: risk.VerdictLikelyAuthentic}
	if sig.VideoOK {
		video := sig.Video
		assessment.Signals.Video = &video
	}
	if sig.AudioOK {
		audio := sig.Audio
		assessment.Signals.Audio = &audio
	}
	if sig.TemporalOK {
		temporal := sig.Temporal
		assessment.Signals.Temporal = &temporal
	}
	return assessment
}

// fakeSessionRepository is an in-memory sessions.Repository, so
// handler-level tests can exercise the create/complete flow without a
// real PostgreSQL instance. See internal/repository/sessions for the
// real implementation and its own tests against a live database.
type fakeSessionRepository struct {
	mu        sync.Mutex
	sessions  map[string]sessionrepo.Session
	createErr error
}

func newFakeSessionRepository() *fakeSessionRepository {
	return &fakeSessionRepository{sessions: make(map[string]sessionrepo.Session)}
}

func (f *fakeSessionRepository) Create(ctx context.Context, s sessionrepo.Session) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
	return nil
}

func (f *fakeSessionRepository) Get(ctx context.Context, id string) (*sessionrepo.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return nil, sessionrepo.ErrNotFound
	}
	return &s, nil
}

func (f *fakeSessionRepository) Complete(ctx context.Context, id string, result sessionrepo.Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return sessionrepo.ErrNotFound
	}
	s.Status = "completed"
	s.EndedAt = &result.CompletedAt
	s.RiskScore = &result.RiskScore
	s.Verdict = result.Verdict
	f.sessions[id] = s
	return nil
}

// fakeAnalysisRepository is an in-memory analysis.Repository, mirroring
// fakeSessionRepository above — see internal/repository/analysis for the
// real implementation and its own tests against a live database.
type fakeAnalysisRepository struct {
	mu      sync.Mutex
	results map[string]analysisrepo.Result
}

func newFakeAnalysisRepository() *fakeAnalysisRepository {
	return &fakeAnalysisRepository{results: make(map[string]analysisrepo.Result)}
}

func (f *fakeAnalysisRepository) Create(ctx context.Context, result analysisrepo.Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[result.SessionID] = result
	return nil
}

func (f *fakeAnalysisRepository) GetBySessionID(ctx context.Context, sessionID string) (*analysisrepo.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[sessionID]
	if !ok {
		return nil, analysisrepo.ErrNotFound
	}
	return &r, nil
}
