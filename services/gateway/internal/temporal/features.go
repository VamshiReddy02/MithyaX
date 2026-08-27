package temporal

// scoreVariance returns the population variance of the frames' fake
// scores: how spread out they are across the clip. A FakeScore is
// always in [0, 1], so the maximum possible variance is 0.25 (half the
// frames at 0, half at 1) — see varianceNormalizer in analyzer.go for
// how that bound turns this into a [0, 1] risk component.
func scoreVariance(frames []Frame) float64 {
	n := len(frames)
	if n == 0 {
		return 0
	}

	mean := 0.0
	for _, f := range frames {
		mean += f.FakeScore
	}
	mean /= float64(n)

	sumSq := 0.0
	for _, f := range frames {
		d := f.FakeScore - mean
		sumSq += d * d
	}
	return sumSq / float64(n)
}

// faceConsistency is the fraction of frames in which a face was
// detected: 1.0 means the face was present in every frame, 0.0 means it
// was never detected. A face that keeps disappearing and reappearing
// pulls this down just as much as one that's mostly absent — this
// measures overall presence, not the pattern of flicker.
func faceConsistency(frames []Frame) float64 {
	n := len(frames)
	if n == 0 {
		return 0
	}

	detected := 0
	for _, f := range frames {
		if f.FaceDetected {
			detected++
		}
	}
	return float64(detected) / float64(n)
}

// spike is one frame whose fake score jumped well above its immediate
// neighbors — the "brief flash of high fakeness surrounded by low
// scores" pattern a manipulated clip can produce even when most frames
// look clean.
type spike struct {
	Index       int
	Timestamp   float64
	Score       float64
	NeighborAvg float64
	Magnitude   float64 // Score - NeighborAvg, always > threshold > 0
}

// detectSpikes finds frames whose fake score exceeds the average of
// their immediate neighbors by more than threshold. The first and last
// frame have only one neighbor and are compared against that alone.
func detectSpikes(frames []Frame, threshold float64) []spike {
	n := len(frames)

	var spikes []spike
	for i, f := range frames {
		var neighborSum float64
		var neighborCount int
		if i > 0 {
			neighborSum += frames[i-1].FakeScore
			neighborCount++
		}
		if i < n-1 {
			neighborSum += frames[i+1].FakeScore
			neighborCount++
		}
		if neighborCount == 0 {
			continue
		}

		neighborAvg := neighborSum / float64(neighborCount)
		magnitude := f.FakeScore - neighborAvg
		if magnitude > threshold {
			spikes = append(spikes, spike{
				Index:       i,
				Timestamp:   f.Timestamp,
				Score:       f.FakeScore,
				NeighborAvg: neighborAvg,
				Magnitude:   magnitude,
			})
		}
	}
	return spikes
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}
