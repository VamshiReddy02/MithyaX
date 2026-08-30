package risk

import "fmt"

// Verdict buckets a risk score into a human-facing category.
type Verdict string

const (
	// VerdictLikelyAuthentic means the combined signals lean real.
	VerdictLikelyAuthentic Verdict = "LIKELY_AUTHENTIC"
	// VerdictSuspicious means the signals are mixed or moderately risky
	// — worth a closer look, not a confident call either way.
	VerdictSuspicious Verdict = "SUSPICIOUS"
	// VerdictLikelyFake means the combined signals lean synthetic.
	VerdictLikelyFake Verdict = "LIKELY_FAKE"
	// VerdictUnknown means neither modality produced a usable signal,
	// so there's no basis for a score-based verdict at all.
	VerdictUnknown Verdict = "UNKNOWN"
)

// Thresholds are the risk-score cutoffs between verdict buckets, each in
// [0, 1]. A score below Suspicious is LIKELY_AUTHENTIC, at or above
// LikelyFake is LIKELY_FAKE, and everything in between is SUSPICIOUS.
type Thresholds struct {
	Suspicious float64
	LikelyFake float64
}

// DefaultThresholds carve out a middle "suspicious" band around the
// per-modality fake/real cutoff (0.5, see DefaultSignalThreshold)
// instead of snapping straight from authentic to fake at one point.
//
// Raised from an original {0.3, 0.6} after real Meet testing (8.3 live
// verification) showed a genuinely real person's combined score
// routinely landing in the 0.4-0.7 range and getting labeled
// "Suspicious"/"Likely Fake" — the video/audio detector models' raw
// per-observation scores on live, doubly-compressed webcam video (Meet
// re-encodes, then the browser re-encodes again to JPEG) run
// noticeably higher than on whatever clean footage they were
// originally evaluated against, and neither these thresholds nor
// DefaultWeights have ever been calibrated against real labeled data
// through this exact pipeline. This change only moves the alarm
// threshold higher to reduce how often that shows up as a false alarm
// on real people — it does not, and cannot, fix the underlying
// per-modality score inflation itself (that needs real evaluation data
// and likely model re-tuning, not a threshold tweak), and moving these
// up necessarily also makes the system slower to flag a real fake
// whose signals happen to land in the band between the old and new
// cutoffs. Replace with real, data-derived values once there's a
// labeled evaluation set collected through this same live pipeline.
var DefaultThresholds = Thresholds{Suspicious: 0.4, LikelyFake: 0.7}

// DefaultSignalThreshold is the per-signal score cutoff above which
// that signal gets called out in Reasons. For video and audio it
// mirrors those detector services' own fake/real threshold, so a reason
// fires exactly when that modality would have called the input "fake"
// on its own; temporal's Score is on the same [0, 1] scale by
// construction, so the same cutoff is reused until evaluation data
// suggests otherwise.
const DefaultSignalThreshold = 0.5

// thresholdEpsilon absorbs floating-point rounding at a threshold
// boundary — found live via a test with a single video signal at
// exactly 0.7 (a natural, "should clearly count" boundary value): a
// weighted-average score meant to land exactly on a threshold can come
// out a hair under it (0.7 / weightedScore's own division here rounds
// to 0.6999999999999998, not 0.7) purely from float64 representation
// error, not from the score genuinely being lower. Without this, a
// score that should conceptually be "at least LikelyFake" could
// silently classify one bucket lower than intended, for reasons that
// have nothing to do with the actual signals.
const thresholdEpsilon = 1e-9

// classify maps a risk score to a Verdict. ok must be the same value
// weightedScore returned alongside score — when false, there was no
// signal to classify at all.
func classify(score float64, ok bool, t Thresholds) Verdict {
	if !ok {
		return VerdictUnknown
	}
	switch {
	case score >= t.LikelyFake-thresholdEpsilon:
		return VerdictLikelyFake
	case score >= t.Suspicious-thresholdEpsilon:
		return VerdictSuspicious
	default:
		return VerdictLikelyAuthentic
	}
}

// buildReasons explains what drove (or limited) the assessment: which
// signals individually crossed the fake threshold, and which signals
// were unavailable and why. Always non-nil, even when empty (a
// low-scoring session with every signal present has nothing to
// report) — analysis_results.risk_reasons is NOT NULL, and a nil
// slice serializes to SQL NULL, not an empty array.
func buildReasons(sig Signals, signalThreshold float64) []string {
	reasons := []string{}

	if sig.AudioOK && sig.Audio >= signalThreshold {
		reasons = append(reasons, "Audio signal indicates likely synthetic speech")
	}
	if sig.VideoOK && sig.Video >= signalThreshold {
		reasons = append(reasons, "Video signal indicates likely synthetic or manipulated content")
	}
	if sig.TemporalOK && sig.Temporal >= signalThreshold {
		reasons = append(reasons, "Temporal signal indicates suspicious frame-to-frame behavior")
	}
	if sig.VideoError != "" {
		reasons = append(reasons, fmt.Sprintf("Video analysis unavailable: %s", sig.VideoError))
	}
	if sig.AudioError != "" {
		reasons = append(reasons, fmt.Sprintf("Audio analysis unavailable: %s", sig.AudioError))
	}
	if sig.TemporalError != "" {
		reasons = append(reasons, fmt.Sprintf("Temporal analysis unavailable: %s", sig.TemporalError))
	}
	if !sig.VideoOK && !sig.AudioOK && !sig.TemporalOK &&
		sig.VideoError == "" && sig.AudioError == "" && sig.TemporalError == "" {
		reasons = append(reasons, "No analysis signals were available")
	}

	return reasons
}
