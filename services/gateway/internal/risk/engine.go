// Package risk turns a completed analysis session's per-modality fake
// scores into a single risk assessment: a weighted score, a verdict
// bucket, and the reasons behind it.
package risk

import "github.com/vamshireddy02/mithyax/gateway/internal/session"

// Signals is the risk engine's input. It's a flat copy of the two
// modality scores rather than *session.AnalysisSession directly, so
// score.go and verdict.go stay simple pure functions that don't need to
// know how the session package models "missing".
type Signals struct {
	Video      float64
	VideoOK    bool
	VideoError string

	Audio      float64
	AudioOK    bool
	AudioError string
}

// FromSession builds Signals from a completed AnalysisSession.
func FromSession(s *session.AnalysisSession) Signals {
	sig := Signals{VideoError: s.VideoError, AudioError: s.AudioError}
	if s.Video != nil {
		sig.Video = s.Video.FakeScore
		sig.VideoOK = true
	}
	if s.Audio != nil {
		sig.Audio = s.Audio.FakeScore
		sig.AudioOK = true
	}
	return sig
}

// SignalScores reports the individual fake scores that fed a risk
// score, omitting whichever modality wasn't available.
type SignalScores struct {
	Video *float64 `json:"video,omitempty"`
	Audio *float64 `json:"audio,omitempty"`
}

// Assessment is the risk engine's output.
type Assessment struct {
	RiskScore float64      `json:"risk_score"`
	Verdict   Verdict      `json:"verdict"`
	Signals   SignalScores `json:"signals"`
	Reasons   []string     `json:"reasons"`
}

// Engine combines whichever modality signals are present into an
// Assessment. It's deterministic and stateless: the same Signals always
// produce the same Assessment.
type Engine struct {
	weights         Weights
	thresholds      Thresholds
	signalThreshold float64
}

// NewEngine builds an Engine using the default weights, verdict
// thresholds, and per-signal reason threshold.
func NewEngine() *Engine {
	return &Engine{
		weights:         DefaultWeights,
		thresholds:      DefaultThresholds,
		signalThreshold: DefaultSignalThreshold,
	}
}

// Assess computes a risk Assessment from a completed AnalysisSession.
func (e *Engine) Assess(s *session.AnalysisSession) Assessment {
	return e.AssessSignals(FromSession(s))
}

// AssessSignals computes a risk Assessment directly from Signals. It's
// the seam engine_test.go uses to exercise the engine's logic without
// having to construct a session.AnalysisSession.
func (e *Engine) AssessSignals(sig Signals) Assessment {
	score, ok := weightedScore(sig.Video, sig.VideoOK, sig.Audio, sig.AudioOK, e.weights)

	assessment := Assessment{
		RiskScore: score,
		Verdict:   classify(score, ok, e.thresholds),
		Reasons:   buildReasons(sig, e.signalThreshold),
	}
	if sig.VideoOK {
		video := sig.Video
		assessment.Signals.Video = &video
	}
	if sig.AudioOK {
		audio := sig.Audio
		assessment.Signals.Audio = &audio
	}
	return assessment
}
