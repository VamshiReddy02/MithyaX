package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

func TestQueue_EnqueueDequeue(t *testing.T) {
	client := newTestRedis(t)
	queue := worker.NewQueue(client, 10)

	if err := queue.Enqueue(context.Background(), "job-1"); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	id, err := queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if id != "job-1" {
		t.Errorf("Dequeue() = %q, want %q", id, "job-1")
	}
}

func TestQueue_FIFOOrder(t *testing.T) {
	client := newTestRedis(t)
	queue := worker.NewQueue(client, 10)

	for _, id := range []string{"a", "b", "c"} {
		if err := queue.Enqueue(context.Background(), id); err != nil {
			t.Fatalf("Enqueue(%q) error = %v", id, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for _, want := range []string{"a", "b", "c"} {
		got, err := queue.Dequeue(ctx)
		if err != nil {
			t.Fatalf("Dequeue() error = %v", err)
		}
		if got != want {
			t.Errorf("Dequeue() = %q, want %q", got, want)
		}
	}
}

func TestQueue_DequeueBlocksUntilEnqueue(t *testing.T) {
	client := newTestRedis(t)
	queue := worker.NewQueue(client, 10)

	go func() {
		time.Sleep(100 * time.Millisecond)
		queue.Enqueue(context.Background(), "job-late")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	id, err := queue.Dequeue(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if id != "job-late" {
		t.Errorf("Dequeue() = %q, want %q", id, "job-late")
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("Dequeue() returned after %v, expected it to actually block until enqueue", elapsed)
	}
}

func TestQueue_DequeueRespectsContextCancellation(t *testing.T) {
	client := newTestRedis(t)
	queue := worker.NewQueue(client, 10)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := queue.Dequeue(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Dequeue() error = nil, want a context-cancellation error")
	}
	if elapsed > time.Second {
		t.Errorf("Dequeue() took %v after cancellation, want it to return promptly", elapsed)
	}
}

func TestQueue_EnqueueFullRejects(t *testing.T) {
	client := newTestRedis(t)
	queue := worker.NewQueue(client, 1)

	if err := queue.Enqueue(context.Background(), "job-1"); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}

	err := queue.Enqueue(context.Background(), "job-2")
	if !errors.Is(err, worker.ErrQueueFull) {
		t.Errorf("second Enqueue() error = %v, want ErrQueueFull", err)
	}
}

func TestQueue_RedisUnavailable(t *testing.T) {
	client := unreachableRedis(t)
	queue := worker.NewQueue(client, 10)

	err := queue.Enqueue(context.Background(), "job-1")
	if !errors.Is(err, worker.ErrRedisUnavailable) {
		t.Errorf("Enqueue() error = %v, want ErrRedisUnavailable", err)
	}
}
