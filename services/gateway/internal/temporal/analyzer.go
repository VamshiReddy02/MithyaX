// Package temporal looks across a video's frames — rather than at any
// one frame in isolation — for patterns a per-frame detector can miss:
// scores that swing wildly, a face that keeps disappearing, or a single
// frame of high fakeness hidden among otherwise-clean ones.
package temporal

import "fmt"

// varianceNormalizer scales scoreVariance (max 0.25, for a maximally
// split 0/1 sequence) up into a [0, 1] risk component.
const varianceNormalizer = 4.0

// Config holds the temporal analyzer's tunable thresholds, kept
// external to Analyzer the same way risk.Weights/risk.Thresholds are —
// so they can be tuned later against real evaluation data without
// touching the analysis logic itself.
type Config struct {
	// SpikeThreshold is how far a frame's fake score must exceed its
	// neighbors' average, in [0, 1], to count as a suspicious spike.
	SpikeThreshold float64
	// VarianceReasonThreshold is the normalized variance component (post
	// varianceNormalizer, in [0, 1]) above which a reason is added
	// explaining the fluctuation.
	VarianceReasonThreshold float64
	// FaceConsistencyThreshold is the consistency ratio below which a
	// reason is added calling out the disappearing face.
	FaceConsistencyThreshold float64
}

// DefaultConfig requires a spike to jump by more than a third of the
// full [0, 1] score range above its neighbors, and flags variance or
// face consistency once they're noticeably worse than mild jitter.
var DefaultConfig = Config{
	SpikeThreshold:           0.35,
	VarianceReasonThreshold:  0.3,
	FaceConsistencyThreshold: 0.8,
}

// Frame is one analyzed video frame's per-frame detector output — the
// temporal analyzer's unit of input.
type Frame struct {
	Timestamp    float64
	FakeScore    float64
	FaceDetected bool
	FaceX        float64
	FaceY        float64
	FaceWidth    float64
	FaceHeight   float64
}

// TemporalResult is the temporal analyzer's output: how suspicious the
// frame-to-frame behavior looked across a clip, and why.
type TemporalResult struct {
	Score           float64
	FramesAnalyzed  int
	FaceConsistency float64
	ScoreVariance   float64
	Reasons         []string
}

// Analyzer computes a TemporalResult from a sequence of Frames. It's
// deterministic and stateless — no HTTP, no database, no ML model —
// which is what lets the frame-to-frame logic be tested independently
// of wherever frames end up coming from.
type Analyzer struct {
	cfg Config
}

// NewAnalyzer builds an Analyzer using DefaultConfig.
func NewAnalyzer() *Analyzer {
	return &Analyzer{cfg: DefaultConfig}
}

// Analyze computes a TemporalResult from frames, which must be in
// chronological order.
//
// It returns nil for an empty slice: with zero frames there's nothing
// to analyze, not even a low-confidence result. A single frame instead
// returns a result with FramesAnalyzed set and a reason explaining that
// it's insufficient — Score, FaceConsistency, and ScoreVariance stay at
// their zero value, since none of them mean anything without at least
// one frame-to-frame comparison.
func (a *Analyzer) Analyze(frames []Frame) *TemporalResult {
	if len(frames) == 0 {
		return nil
	}
	if len(frames) == 1 {
		return &TemporalResult{
			FramesAnalyzed: 1,
			Reasons:        []string{"insufficient temporal data: at least 2 frames are required"},
		}
	}

	variance := scoreVariance(frames)
	consistency := faceConsistency(frames)
	spikes := detectSpikes(frames, a.cfg.SpikeThreshold)

	varianceComponent := clamp01(variance * varianceNormalizer)
	inconsistencyComponent := clamp01(1 - consistency)
	spikeComponent := spikeMagnitudeComponent(spikes)

	score := clamp01((varianceComponent + inconsistencyComponent + spikeComponent) / 3)

	return &TemporalResult{
		Score:           score,
		FramesAnalyzed:  len(frames),
		FaceConsistency: consistency,
		ScoreVariance:   variance,
		Reasons:         buildReasons(variance, varianceComponent, consistency, spikes, a.cfg),
	}
}

// spikeMagnitudeComponent turns however many spikes were found into a
// single [0, 1] risk component, driven by the single worst spike — one
// dramatic jump is exactly as suspicious whether it's the only anomaly
// in the clip or one of several.
func spikeMagnitudeComponent(spikes []spike) float64 {
	max := 0.0
	for _, s := range spikes {
		if s.Magnitude > max {
			max = s.Magnitude
		}
	}
	return clamp01(max)
}

func buildReasons(variance, varianceComponent, consistency float64, spikes []spike, cfg Config) []string {
	var reasons []string

	if varianceComponent >= cfg.VarianceReasonThreshold {
		reasons = append(reasons, fmt.Sprintf("Fake score fluctuates significantly across frames (variance=%.3f)", variance))
	}
	if consistency < cfg.FaceConsistencyThreshold {
		reasons = append(reasons, fmt.Sprintf("Face detected in only %.0f%% of frames", consistency*100))
	}
	for _, s := range spikes {
		reasons = append(reasons, fmt.Sprintf(
			"Suspicious score spike at frame %d (t=%.2f): score %.2f vs neighboring average %.2f",
			s.Index+1, s.Timestamp, s.Score, s.NeighborAvg,
		))
	}

	return reasons
}
