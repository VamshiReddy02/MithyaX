package queue_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/queue"
)

// Compile-time proof Redis actually satisfies Queue — the whole point
// of the interface (see queue.go) is that callers depend on Queue, not
// on this concrete type.
var _ queue.Queue = (*queue.Redis)(nil)

func TestRedis_EnqueueDequeue(t *testing.T) {
	client := newTestRedis(t)
	q := queue.NewRedis(client, "test:queue", 10)

	job := queue.Job{ID: "job-1", Type: "video_analysis", Payload: []byte("hello"), CreatedAt: time.Now().UTC()}
	if err := q.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if got.ID != job.ID || got.Type != job.Type || string(got.Payload) != string(job.Payload) {
		t.Errorf("Dequeue() = %+v, want %+v", got, job)
	}
	if !got.CreatedAt.Equal(job.CreatedAt) {
		t.Errorf("Dequeue().CreatedAt = %v, want %v", got.CreatedAt, job.CreatedAt)
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
		got, err := q.Dequeue(ctx)
		if err != nil {
			t.Fatalf("Dequeue() error = %v", err)
		}
		if got.ID != want {
			t.Errorf("Dequeue() = %q, want %q", got.ID, want)
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
	got, err := q.Dequeue(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if got.ID != "job-late" {
		t.Errorf("Dequeue() = %q, want %q", got.ID, "job-late")
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("Dequeue() returned after %v, expected it to actually block until enqueue", elapsed)
	}
}

// TestRedis_DequeueOutlastsOneEmptyPoll proves Dequeue keeps waiting
// past a single BLPOP timeout (dequeueBlock) rather than giving up the
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

	got, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v, want it to have kept polling past one empty BLPOP timeout", err)
	}
	if got.ID != "job-very-late" {
		t.Errorf("Dequeue() = %q, want %q", got.ID, "job-very-late")
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
	got, err := videoQueue.Dequeue(ctx2)
	if err != nil || got.ID != "video-job" {
		t.Errorf("videoQueue.Dequeue() = (%+v, %v), want (video-job, nil)", got, err)
	}
}

// TestRedis_GatewayTestRedisURL exercises the queue against a real,
// separately-running Redis instance named by GATEWAY_TEST_REDIS_URL,
// skipping (not failing) if that isn't set — proving the full
// "Gateway → Queue interface → Redis implementation" chain the 7.3.5
// success condition asks for, not just miniredis's protocol emulation.
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

	q := queue.NewRedis(client, "mithyax:test:queue", 10)
	t.Cleanup(func() { client.Del(context.Background(), "mithyax:test:queue") })

	job := queue.Job{ID: "live-job", Type: "video_analysis", Payload: []byte("real redis")}
	if err := q.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	ctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	got, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if got.ID != job.ID || got.Type != job.Type || string(got.Payload) != string(job.Payload) {
		t.Errorf("Dequeue() = %+v, want %+v", got, job)
	}
}
