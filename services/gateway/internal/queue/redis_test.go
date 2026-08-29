package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
)

// Compile-time proof Redis actually satisfies Queue — the whole point
// of the interface (see queue.go) is that callers depend on Queue, not
// on this concrete type.
var _ queue.Queue = (*queue.Redis)(nil)

func TestRedis_EnqueueDequeueAck(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	job := queue.Job{ID: "job-1", Type: "video_analysis", Payload: []byte("hello"), CreatedAt: time.Now().UTC()}
	if err := q.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	d, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	got := d.Job
	if got.ID != job.ID || got.Type != job.Type || string(got.Payload) != string(job.Payload) {
		t.Errorf("Dequeue().Job = %+v, want %+v", got, job)
	}
	if !got.CreatedAt.Equal(job.CreatedAt) {
		t.Errorf("Dequeue().Job.CreatedAt = %v, want %v", got.CreatedAt, job.CreatedAt)
	}

	if err := q.Ack(context.Background(), d); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
}

func TestRedis_FIFOOrder(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	for _, id := range []string{"a", "b", "c"} {
		if err := q.Enqueue(context.Background(), queue.Job{ID: id}); err != nil {
			t.Fatalf("Enqueue(%q) error = %v", id, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for _, want := range []string{"a", "b", "c"} {
		d, err := q.Dequeue(ctx)
		if err != nil {
			t.Fatalf("Dequeue() error = %v", err)
		}
		if d.Job.ID != want {
			t.Errorf("Dequeue().Job.ID = %q, want %q", d.Job.ID, want)
		}
	}
}

func TestRedis_DequeueBlocksUntilEnqueue(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	go func() {
		time.Sleep(100 * time.Millisecond)
		q.Enqueue(context.Background(), queue.Job{ID: "job-late"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	d, err := q.Dequeue(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if d.Job.ID != "job-late" {
		t.Errorf("Dequeue().Job.ID = %q, want %q", d.Job.ID, "job-late")
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("Dequeue() returned after %v, expected it to actually block until enqueue", elapsed)
	}
}

// TestRedis_DequeueOutlastsOneEmptyPoll proves Dequeue keeps waiting
// past a single BLMOVE timeout (dequeueBlock) rather than giving up the
// first time nothing's queued yet — the "no job yet" case must be
// invisible to callers, not surfaced as an error they have to retry on.
func TestRedis_DequeueOutlastsOneEmptyPoll(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	go func() {
		time.Sleep(2500 * time.Millisecond) // longer than dequeueBlock (2s)
		q.Enqueue(context.Background(), queue.Job{ID: "job-very-late"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	d, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v, want it to have kept polling past one empty BLMOVE timeout", err)
	}
	if d.Job.ID != "job-very-late" {
		t.Errorf("Dequeue().Job.ID = %q, want %q", d.Job.ID, "job-very-late")
	}
}

func TestRedis_DequeueRespectsContextCancellation(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := q.Dequeue(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Dequeue() error = nil, want a context-cancellation error")
	}
	if elapsed > time.Second {
		t.Errorf("Dequeue() took %v after cancellation, want it to return promptly", elapsed)
	}
}

func TestRedis_EnqueueFullRejects(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 1)

	if err := q.Enqueue(context.Background(), queue.Job{ID: "job-1"}); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}

	err := q.Enqueue(context.Background(), queue.Job{ID: "job-2"})
	if !errors.Is(err, queue.ErrQueueFull) {
		t.Errorf("second Enqueue() error = %v, want ErrQueueFull", err)
	}
}

func TestRedis_EnqueueRedisUnavailable(t *testing.T) {
	client := unreachableRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	err := q.Enqueue(context.Background(), queue.Job{ID: "job-1"})
	if !errors.Is(err, queue.ErrRedisUnavailable) {
		t.Errorf("Enqueue() error = %v, want ErrRedisUnavailable", err)
	}
}

// TestRedis_DequeueRedisUnavailable proves a Redis outage surfaces as
// ErrRedisUnavailable on Dequeue too, not just Enqueue — a worker
// (Phase 7.5) needs to tell "Redis is down" apart from "nothing queued
// yet" the same way it already can for enqueueing.
func TestRedis_DequeueRedisUnavailable(t *testing.T) {
	client := unreachableRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := q.Dequeue(ctx)
	if !errors.Is(err, queue.ErrRedisUnavailable) {
		t.Errorf("Dequeue() error = %v, want ErrRedisUnavailable", err)
	}
}

// TestRedis_SeparateKeysDoNotInterfere proves two Queue instances
// backed by different keys on the same Redis are independent — the
// point of taking key as a parameter, ahead of Phase 7.5 giving each
// job Type its own queue.
func TestRedis_SeparateKeysDoNotInterfere(t *testing.T) {
	client := newTestRedis(t)
	videoQueue := queue.NewRedis(client, "test:video", 10)
	audioQueue := queue.NewRedis(client, "test:audio", 10)

	if err := videoQueue.Enqueue(context.Background(), queue.Job{ID: "video-job"}); err != nil {
		t.Fatalf("videoQueue.Enqueue() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := audioQueue.Dequeue(ctx); err == nil {
		t.Fatal("audioQueue.Dequeue() returned a job enqueued on videoQueue's key")
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	d, err := videoQueue.Dequeue(ctx2)
	if err != nil || d.Job.ID != "video-job" {
		t.Errorf("videoQueue.Dequeue() = (%+v, %v), want (video-job, nil)", d, err)
	}
}

// --- 7.4.3: acknowledgment semantics ---

func TestRedis_Ack_RemovesFromProcessing(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)
	ctx := context.Background()

	if err := q.Enqueue(ctx, queue.Job{ID: "job-1"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	d, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}

	if n, err := q.ProcessingLen(ctx); err != nil || n != 1 {
		t.Fatalf("ProcessingLen() = (%d, %v), want (1, nil) after Dequeue before Ack", n, err)
	}

	if err := q.Ack(ctx, d); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}

	if n, err := q.ProcessingLen(ctx); err != nil || n != 0 {
		t.Errorf("ProcessingLen() = (%d, %v), want (0, nil) after Ack", n, err)
	}
}

// TestRedis_CrashedConsumer_JobSurvivesInProcessing is the core proof
// behind 7.4.3: "if the worker crashes after receiving, the job
// shouldn't disappear permanently." Dequeue without ever acking or
// failing simulates exactly that crash — the job must still exist
// somewhere durable (the processing list), not have vanished.
func TestRedis_CrashedConsumer_JobSurvivesInProcessing(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)
	ctx := context.Background()

	if err := q.Enqueue(ctx, queue.Job{ID: "job-1", Payload: []byte("important")}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	d, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	// ... the worker crashes here, before Ack or Fail ...

	if n, err := q.PendingLen(ctx); err != nil || n != 0 {
		t.Errorf("PendingLen() = (%d, %v), want (0, nil) — the job left the pending list", n, err)
	}
	if n, err := q.ProcessingLen(ctx); err != nil || n != 1 {
		t.Fatalf("ProcessingLen() = (%d, %v), want (1, nil) — a crashed worker must not lose the job", n, err)
	}
	if d.Job.ID != "job-1" || string(d.Job.Payload) != "important" {
		t.Errorf("the surviving job's own data = %+v, want ID=job-1 Payload=important", d.Job)
	}
}

// TestRedis_RecoverStale proves a job stranded in processing by a
// crashed consumer (see the test above) is actually recoverable, not
// just inspectable: after RecoverStale, it's back in pending and a
// completely fresh Dequeue picks it up like any other job.
func TestRedis_RecoverStale(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)
	ctx := context.Background()

	if err := q.Enqueue(ctx, queue.Job{ID: "stuck-job"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := q.Dequeue(ctx); err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	// ... crash, no Ack/Fail ...
	if n, _ := q.ProcessingLen(ctx); n != 1 {
		t.Fatalf("ProcessingLen() = %d, want 1 before recovery", n)
	}

	recovered, err := q.RecoverStale(ctx)
	if err != nil {
		t.Fatalf("RecoverStale() error = %v", err)
	}
	if recovered != 1 {
		t.Errorf("RecoverStale() recovered %d jobs, want 1", recovered)
	}
	if n, _ := q.ProcessingLen(ctx); n != 0 {
		t.Errorf("ProcessingLen() = %d after RecoverStale, want 0", n)
	}
	if n, _ := q.PendingLen(ctx); n != 1 {
		t.Errorf("PendingLen() = %d after RecoverStale, want 1", n)
	}

	dctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	d, err := q.Dequeue(dctx)
	if err != nil {
		t.Fatalf("Dequeue() after recovery error = %v", err)
	}
	if d.Job.ID != "stuck-job" {
		t.Errorf("Dequeue() after recovery = %q, want %q", d.Job.ID, "stuck-job")
	}
}

func TestRedis_RecoverStale_NothingToRecover(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	recovered, err := q.RecoverStale(context.Background())
	if err != nil {
		t.Fatalf("RecoverStale() error = %v", err)
	}
	if recovered != 0 {
		t.Errorf("RecoverStale() recovered %d jobs, want 0", recovered)
	}
}

func TestRedis_Fail_MovesToFailedList(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)
	ctx := context.Background()

	if err := q.Enqueue(ctx, queue.Job{ID: "job-1", Type: "VIDEO_ANALYSIS"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	d, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}

	if err := q.Fail(ctx, d, "video-detector unreachable"); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	if n, err := q.ProcessingLen(ctx); err != nil || n != 0 {
		t.Errorf("ProcessingLen() = (%d, %v), want (0, nil) after Fail", n, err)
	}
	if n, err := q.FailedLen(ctx); err != nil || n != 1 {
		t.Fatalf("FailedLen() = (%d, %v), want (1, nil) after Fail", n, err)
	}

	raw, err := client.LIndex(ctx, "test:queue:failed", 0).Result()
	if err != nil {
		t.Fatalf("LIndex on failed list: %v", err)
	}
	var entry queue.FailedDelivery
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal FailedDelivery: %v", err)
	}
	if entry.Job.ID != "job-1" {
		t.Errorf("FailedDelivery.Job.ID = %q, want %q", entry.Job.ID, "job-1")
	}
	if entry.Reason != "video-detector unreachable" {
		t.Errorf("FailedDelivery.Reason = %q, want %q", entry.Reason, "video-detector unreachable")
	}
	if entry.FailedAt.IsZero() {
		t.Error("FailedDelivery.FailedAt is zero, want it set")
	}
}

// --- 7.4.4/7.4.6: idempotency (duplicate job ID) ---

// TestRedis_DuplicateJobID proves the queue is at-least-once, not
// exactly-once, and is explicit about it: two jobs sharing an ID (the
// same job enqueued twice — e.g. by a redelivery a future retry
// scheduler triggers) are both stored and both delivered rather than
// the queue silently deduplicating or corrupting either. Making sure
// the SAME analysis doesn't get double-recorded is the consumer's job,
// using that shared ID — see analysisjob.AnalysisJob's doc comment.
func TestRedis_DuplicateJobID(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)
	ctx := context.Background()

	job := queue.Job{ID: "dup-job", Payload: []byte("v1")}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("second Enqueue() (duplicate ID) error = %v", err)
	}

	dctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	first, err := q.Dequeue(dctx)
	if err != nil {
		t.Fatalf("first Dequeue() error = %v", err)
	}
	second, err := q.Dequeue(dctx)
	if err != nil {
		t.Fatalf("second Dequeue() error = %v", err)
	}

	if first.Job.ID != "dup-job" || second.Job.ID != "dup-job" {
		t.Fatalf("expected both deliveries to carry ID dup-job, got %q and %q", first.Job.ID, second.Job.ID)
	}

	// Both deliveries must be independently acknowledgeable — acking
	// one must not silently also ack (or fail to find) the other.
	if err := q.Ack(ctx, first); err != nil {
		t.Fatalf("Ack(first) error = %v", err)
	}
	if n, _ := q.ProcessingLen(ctx); n != 1 {
		t.Fatalf("ProcessingLen() = %d after acking one of two duplicates, want 1 (the other still in flight)", n)
	}
	if err := q.Ack(ctx, second); err != nil {
		t.Fatalf("Ack(second) error = %v", err)
	}
	if n, _ := q.ProcessingLen(ctx); n != 0 {
		t.Errorf("ProcessingLen() = %d after acking both duplicates, want 0", n)
	}
}

// --- 7.4.6: malformed / empty jobs ---

// TestRedis_MalformedEntry_DequeueReturnsError proves a corrupt entry
// in the pending list (never possible via this package's own Enqueue,
// but plausible from a version skew or a manual redis-cli mistake)
// surfaces as a clear error rather than a panic or a silently-dropped
// job.
func TestRedis_MalformedEntry_DequeueReturnsError(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)
	ctx := context.Background()

	if err := client.RPush(ctx, "test:queue", "not valid json").Err(); err != nil {
		t.Fatalf("RPush malformed entry: %v", err)
	}

	dctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := q.Dequeue(dctx); err == nil {
		t.Fatal("Dequeue() of a malformed entry: expected an error, got nil")
	}

	// The malformed entry should still have moved to processing (BLMOVE
	// already happened before decoding failed) rather than being lost
	// entirely or stuck retriggering the same failure forever from
	// pending.
	if n, err := q.ProcessingLen(ctx); err != nil || n != 1 {
		t.Errorf("ProcessingLen() = (%d, %v), want (1, nil) — the malformed entry should be visible for inspection, not gone", n, err)
	}
}

func TestRedis_EmptyJob_RoundTrips(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)
	ctx := context.Background()

	if err := q.Enqueue(ctx, queue.Job{}); err != nil {
		t.Fatalf("Enqueue(zero-value Job) error = %v", err)
	}

	dctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	d, err := q.Dequeue(dctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if d.Job.ID != "" || d.Job.Type != "" || len(d.Job.Payload) != 0 {
		t.Errorf("Dequeue().Job = %+v, want the zero value round-tripped unchanged", d.Job)
	}
}

// --- 7.4.6: concurrency ---

func TestRedis_MultipleProducers(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 100)
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := q.Enqueue(ctx, queue.Job{ID: fmt.Sprintf("job-%d", i)})
			if err != nil {
				t.Errorf("Enqueue(job-%d) error = %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for i := 0; i < n; i++ {
		d, err := q.Dequeue(dctx)
		if err != nil {
			t.Fatalf("Dequeue() #%d error = %v", i, err)
		}
		if seen[d.Job.ID] {
			t.Errorf("job %q delivered more than once", d.Job.ID)
		}
		seen[d.Job.ID] = true
	}
	if len(seen) != n {
		t.Errorf("received %d distinct jobs, want %d", len(seen), n)
	}
}

// TestRedis_MultipleConsumers proves N jobs fanned out across M
// concurrent consumers are each delivered exactly once in total — no
// job silently dropped, none double-delivered under normal (non-crash)
// operation.
func TestRedis_MultipleConsumers(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 100)
	ctx := context.Background()

	const numJobs = 30
	const numConsumers = 5

	for i := 0; i < numJobs; i++ {
		if err := q.Enqueue(ctx, queue.Job{ID: fmt.Sprintf("job-%d", i)}); err != nil {
			t.Fatalf("Enqueue(job-%d) error = %v", i, err)
		}
	}

	var mu sync.Mutex
	received := make(map[string]int, numJobs)
	var wg sync.WaitGroup
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	for c := 0; c < numConsumers; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				d, err := q.Dequeue(dctx)
				if err != nil {
					return // context deadline once the queue's drained — expected
				}
				mu.Lock()
				received[d.Job.ID]++
				mu.Unlock()
				q.Ack(context.Background(), d)
			}
		}()
	}
	wg.Wait()

	if len(received) != numJobs {
		t.Errorf("received %d distinct jobs across %d consumers, want %d", len(received), numConsumers, numJobs)
	}
	for id, count := range received {
		if count != 1 {
			t.Errorf("job %q delivered %d times, want exactly 1", id, count)
		}
	}
}

// --- live Redis ---

// TestRedis_GatewayTestRedisURL exercises the queue (including ack)
// against a real, separately-running Redis instance named by
// GATEWAY_TEST_REDIS_URL, skipping (not failing) if that isn't set —
// proving the full "Gateway → Queue interface → Redis implementation"
// chain, not just miniredis's protocol emulation.
func TestRedis_GatewayTestRedisURL(t *testing.T) {
	testRedisURL := os.Getenv("GATEWAY_TEST_REDIS_URL")
	if testRedisURL == "" {
		t.Skip("GATEWAY_TEST_REDIS_URL not set; skipping test that needs a real Redis instance (see deployments/docker/docker-compose.yml)")
	}

	opts, err := goredis.ParseURL(testRedisURL)
	if err != nil {
		t.Fatalf("parse GATEWAY_TEST_REDIS_URL: %v", err)
	}
	client := goredis.NewClient(opts)
	defer client.Close()

	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		t.Skipf("Redis not reachable at %s: %v", testRedisURL, err)
	}

	key := "mithyax:test:queue-74"
	q := queue.NewRedis(client, key, 10)
	t.Cleanup(func() {
		ctx := context.Background()
		client.Del(ctx, key, key+":processing", key+":failed")
	})

	job := queue.Job{ID: "live-job", Type: "video_analysis", Payload: []byte("real redis")}
	if err := q.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	ctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	d, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if d.Job.ID != job.ID || d.Job.Type != job.Type || string(d.Job.Payload) != string(job.Payload) {
		t.Errorf("Dequeue().Job = %+v, want %+v", d.Job, job)
	}

	if err := q.Ack(context.Background(), d); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	if n, err := q.ProcessingLen(context.Background()); err != nil || n != 0 {
		t.Errorf("ProcessingLen() = (%d, %v), want (0, nil) after Ack against real Redis", n, err)
	}
}
