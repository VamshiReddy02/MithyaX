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
var DefaultThresholds = Thresholds{Suspicious: 0.3, LikelyFake: 0.6}

// DefaultSignalThreshold is the per-modality fake-score cutoff above
// which that modality's own signal gets called out in Reasons. It
// mirrors the audio- and video-detector services' own fake/real
// threshold, so a reason fires exactly when that modality would have
// called the input "fake" on its own.
const DefaultSignalThreshold = 0.5

// classify maps a risk score to a Verdict. ok must be the same value
// weightedScore returned alongside score — when false, there was no
// signal to classify at all.
func classify(score float64, ok bool, t Thresholds) Verdict {
	if !ok {
		return VerdictUnknown
	}
	switch {
	case score >= t.LikelyFake:
		return VerdictLikelyFake
	case score >= t.Suspicious:
		return VerdictSuspicious
	default:
		return VerdictLikelyAuthentic
	}
}

// buildReasons explains what drove (or limited) the assessment: which
// modalities individually crossed the fake threshold, and which
// modalities were unavailable and why.
func buildReasons(sig Signals, signalThreshold float64) []string {
	var reasons []string

	if sig.AudioOK && sig.Audio >= signalThreshold {
		reasons = append(reasons, "Audio signal indicates likely synthetic speech")
	}
	if sig.VideoOK && sig.Video >= signalThreshold {
		reasons = append(reasons, "Video signal indicates likely synthetic or manipulated content")
	}
	if sig.VideoError != "" {
		reasons = append(reasons, fmt.Sprintf("Video analysis unavailable: %s", sig.VideoError))
	}
	if sig.AudioError != "" {
		reasons = append(reasons, fmt.Sprintf("Audio analysis unavailable: %s", sig.AudioError))
	}
	if !sig.VideoOK && !sig.AudioOK && sig.VideoError == "" && sig.AudioError == "" {
		reasons = append(reasons, "No analysis signals were available")
	}

	return reasons
}
