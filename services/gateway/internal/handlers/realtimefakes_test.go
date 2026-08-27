package handlers_test

import (
	"context"

	"github.com/vamshireddy02/mithyax/gateway/internal/audio"
	"github.com/vamshireddy02/mithyax/gateway/internal/detector"
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

func (f *fakeRealtimeRiskEngine) AssessSignals(sig risk.Signals) risk.Assessment {
	return risk.Assessment{RiskScore: 0.1, Verdict: risk.VerdictLikelyAuthentic}
}
