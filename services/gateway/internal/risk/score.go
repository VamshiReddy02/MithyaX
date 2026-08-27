package risk

// Weights controls how much each modality's fake score contributes to
// the combined risk score when both are present. They don't need to sum
// to 1 — weightedScore normalizes by whatever weights are actually in
// play, which also lets a missing modality's weight drop out cleanly
// rather than get redistributed by hand.
type Weights struct {
	Video float64
	Audio float64
}

// DefaultWeights weighs video and audio equally: neither modality is
// inherently more trustworthy than the other, so absent a reason to
// think otherwise, a fake signal from either counts the same.
var DefaultWeights = Weights{Video: 0.5, Audio: 0.5}

// weightedScore combines whichever fake scores are present into a
// single risk score in [0, 1]. A modality that's missing — not
// requested, or its analysis failed — is excluded entirely rather than
// treated as a 0 (authentic): that would let a missing video signal
// quietly dilute a damning audio score, or vice versa. ok is false only
// when neither modality is present, since there's then nothing to base
// a score on.
func weightedScore(video float64, hasVideo bool, audio float64, hasAudio bool, weights Weights) (score float64, ok bool) {
	switch {
	case hasVideo && hasAudio:
		total := weights.Video + weights.Audio
		if total <= 0 {
			return 0, false
		}
		return (video*weights.Video + audio*weights.Audio) / total, true
	case hasVideo:
		return video, true
	case hasAudio:
		return audio, true
	default:
		return 0, false
	}
}
