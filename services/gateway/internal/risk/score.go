package risk

// Weights controls how much each signal contributes to the combined
// risk score when it's present. They don't need to sum to 1 —
// weightedScore normalizes by whatever weights are actually in play,
// which also lets a missing signal's weight drop out cleanly rather
// than get redistributed by hand.
type Weights struct {
	Video    float64
	Audio    float64
	Temporal float64
}

// DefaultWeights favor video and audio — each backed by a dedicated
// fake-detection model run over the whole clip — over temporal, which
// only looks at frame-to-frame consistency and so is a weaker
// standalone signal: more useful for corroborating (or undercutting)
// the other two than for carrying a verdict on its own. These are
// starting weights, not yet tuned against real evaluation data.
var DefaultWeights = Weights{Video: 0.40, Audio: 0.35, Temporal: 0.25}

// signalInput pairs one signal's score with whether it's actually
// present and how much it should count, for weightedScore to combine.
type signalInput struct {
	score   float64
	present bool
	weight  float64
}

// weightedScore combines whichever signals are present into a single
// risk score in [0, 1]. A signal that's missing — not requested, or its
// analysis failed — is excluded entirely rather than treated as a 0
// (authentic): that would let a missing signal quietly dilute the
// signals that did report. ok is false only when no signal at all is
// present (or the present ones carry zero total weight), since there's
// then nothing to base a score on.
func weightedScore(inputs ...signalInput) (score float64, ok bool) {
	var weightedSum, totalWeight float64
	for _, in := range inputs {
		if !in.present {
			continue
		}
		weightedSum += in.score * in.weight
		totalWeight += in.weight
	}
	if totalWeight <= 0 {
		return 0, false
	}
	return weightedSum / totalWeight, true
}
