package analysisworker

import (
	"context"
	"log/slog"

	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
)

// Pool runs a fixed number of Workers against one Queue, all sharing
// one Handler and one Metrics — the "Video Queue → Worker 1/2/3" and
// "Audio Queue → Worker 1/2" fan-out from 7.5.4's diagram. Concurrency
// is fixed at construction (GATEWAY_VIDEO_WORKERS / GATEWAY_AUDIO_WORKERS),
// not one goroutine per job.
type Pool struct {
	workers []*Worker
	metrics *Metrics
}

// NewPool builds a Pool of count Workers, all consuming from q via
// handler. Workers don't start doing anything until Start is called.
func NewPool(q queue.Queue, handler Handler, count int, logger *slog.Logger, opts ...Option) *Pool {
	metrics := NewMetrics()
	workers := make([]*Worker, count)
	for i := range workers {
		workers[i] = NewWorker(q, handler, metrics, logger, opts...)
	}
	return &Pool{workers: workers, metrics: metrics}
}

// Start launches every worker in the pool. ctx bounds their lifetime
// the same way it does for a single Worker (see Worker.Start).
func (p *Pool) Start(ctx context.Context) {
	for _, w := range p.workers {
		w.Start(ctx)
	}
}

// Stop stops every worker gracefully, waiting for each to finish its
// current job (if any) before returning. Workers are stopped
// concurrently rather than one after another, so total shutdown time
// is bounded by the slowest single in-flight job, not the sum of all
// of them.
func (p *Pool) Stop() {
	done := make(chan struct{}, len(p.workers))
	for _, w := range p.workers {
		go func(w *Worker) {
			w.Stop()
			done <- struct{}{}
		}(w)
	}
	for range p.workers {
		<-done
	}
}

// Metrics returns a point-in-time snapshot of this pool's counters —
// summed across every worker sharing it (see NewPool).
func (p *Pool) Metrics() MetricsSnapshot {
	return p.metrics.Snapshot()
}
