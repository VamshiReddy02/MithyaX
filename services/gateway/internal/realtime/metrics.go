package realtime

import (
	"sync/atomic"
	"time"
)

// Metrics is a process-wide, thread-safe counter/gauge set for the live
// session pipeline, shared by every Session a Store creates. It exists
// to answer "is the realtime pipeline keeping up" operationally — see
// MetricsSnapshot for what's exposed (GET /api/v1/sessions/metrics). It
// deliberately isn't tied to a specific metrics backend (Prometheus,
// StatsD, ...): that's Phase 10 (Observability) to decide; this just
// makes the numbers available for it to wire up.
type Metrics struct {
	framesReceived  atomic.Int64
	framesProcessed atomic.Int64
	framesDropped   atomic.Int64

	audioChunksReceived  atomic.Int64
	audioChunksProcessed atomic.Int64
	audioChunksDropped   atomic.Int64

	videoQueueDepth atomic.Int64
	audioQueueDepth atomic.Int64

	videoLatency latencyStats
	audioLatency latencyStats
}

func newMetrics() *Metrics {
	return &Metrics{}
}

// latencyStats accumulates a count, running total, and max for a simple
// average/worst-case latency reading, without needing a histogram.
type latencyStats struct {
	count   atomic.Int64
	totalMs atomic.Int64
	maxMs   atomic.Int64
}

func (l *latencyStats) observe(d time.Duration) {
	ms := d.Milliseconds()
	l.count.Add(1)
	l.totalMs.Add(ms)

	for {
		cur := l.maxMs.Load()
		if ms <= cur {
			return
		}
		if l.maxMs.CompareAndSwap(cur, ms) {
			return
		}
	}
}

func (l *latencyStats) snapshot() LatencySnapshot {
	count := l.count.Load()
	var avg float64
	if count > 0 {
		avg = float64(l.totalMs.Load()) / float64(count)
	}
	return LatencySnapshot{Count: count, AverageMs: avg, MaxMs: l.maxMs.Load()}
}

// LatencySnapshot is one point-in-time read of a latencyStats.
type LatencySnapshot struct {
	Count     int64   `json:"count"`
	AverageMs float64 `json:"average_ms"`
	MaxMs     int64   `json:"max_ms"`
}

// MetricsSnapshot is a point-in-time, JSON-marshalable read of Metrics.
type MetricsSnapshot struct {
	FramesReceived  int64 `json:"frames_received"`
	FramesProcessed int64 `json:"frames_processed"`
	FramesDropped   int64 `json:"frames_dropped"`

	AudioChunksReceived  int64 `json:"audio_chunks_received"`
	AudioChunksProcessed int64 `json:"audio_chunks_processed"`
	AudioChunksDropped   int64 `json:"audio_chunks_dropped"`

	VideoQueueDepth int64 `json:"video_queue_depth"`
	AudioQueueDepth int64 `json:"audio_queue_depth"`

	VideoInferenceLatency LatencySnapshot `json:"video_inference_latency"`
	AudioInferenceLatency LatencySnapshot `json:"audio_inference_latency"`
}

// Snapshot reads every counter/gauge at once. The read isn't atomic
// across fields (there's no single lock covering all of them), so under
// concurrent updates it's a "close enough" instant rather than a
// perfectly consistent one — the standard, accepted tradeoff for
// lock-free metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		FramesReceived:  m.framesReceived.Load(),
		FramesProcessed: m.framesProcessed.Load(),
		FramesDropped:   m.framesDropped.Load(),

		AudioChunksReceived:  m.audioChunksReceived.Load(),
		AudioChunksProcessed: m.audioChunksProcessed.Load(),
		AudioChunksDropped:   m.audioChunksDropped.Load(),

		VideoQueueDepth: m.videoQueueDepth.Load(),
		AudioQueueDepth: m.audioQueueDepth.Load(),

		VideoInferenceLatency: m.videoLatency.snapshot(),
		AudioInferenceLatency: m.audioLatency.snapshot(),
	}
}
