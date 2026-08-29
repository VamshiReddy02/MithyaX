package analysisworker

import (
	"sync/atomic"
	"time"
)

// Metrics is a process-wide, thread-safe counter/gauge set for the
// async analysis worker pools, shared by every Worker a Pool starts —
// the same role internal/realtime.Metrics plays for the live session
// pipeline, and deliberately following the same shape (plain
// atomics + a lightweight latencyStats, no backend-specific type) so
// Phase 10's Prometheus wiring can treat both the same way.
type Metrics struct {
	jobsReceived     atomic.Int64
	jobsCompleted    atomic.Int64
	jobsFailed       atomic.Int64
	jobsRetried      atomic.Int64
	jobsDeadLettered atomic.Int64
	activeWorkers    atomic.Int64

	processingLatency latencyStats
	queueWaitLatency  latencyStats
}

// NewMetrics builds an empty Metrics. Exported (unlike
// internal/realtime's private newMetrics) because a Pool is
// constructed once per job type (video, audio) by httpserver, which
// needs to hand each its own Metrics to expose separately.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// latencyStats accumulates a count, running total, and max for a simple
// average/worst-case latency reading — copied from internal/realtime's
// identical type rather than shared, since neither package has any
// other reason to depend on the other.
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
	JobsReceived     int64 `json:"jobs_received"`
	JobsCompleted    int64 `json:"jobs_completed"`
	JobsFailed       int64 `json:"jobs_failed"`
	JobsRetried      int64 `json:"jobs_retried"`
	JobsDeadLettered int64 `json:"jobs_dead_lettered"`
	ActiveWorkers    int64 `json:"active_workers"`

	ProcessingLatency LatencySnapshot `json:"processing_latency"`
	QueueWaitLatency  LatencySnapshot `json:"queue_wait_latency"`
}

// Snapshot reads every counter/gauge at once. As with
// internal/realtime.Metrics, this isn't atomic across fields — a
// "close enough" instant, the standard tradeoff for lock-free metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		JobsReceived:     m.jobsReceived.Load(),
		JobsCompleted:    m.jobsCompleted.Load(),
		JobsFailed:       m.jobsFailed.Load(),
		JobsRetried:      m.jobsRetried.Load(),
		JobsDeadLettered: m.jobsDeadLettered.Load(),
		ActiveWorkers:    m.activeWorkers.Load(),

		ProcessingLatency: m.processingLatency.snapshot(),
		QueueWaitLatency:  m.queueWaitLatency.snapshot(),
	}
}
