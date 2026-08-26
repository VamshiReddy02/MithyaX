package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/worker"
)

func TestStore_PutGet(t *testing.T) {
	client := newTestRedis(t)
	store := worker.NewStore(client, time.Hour)
	job := worker.Job{ID: "abc", VideoURL: "https://example.com/v.mp4", Status: worker.StatusQueued}

	if err := store.Put(context.Background(), job); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, err := store.Get(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != job.ID || got.VideoURL != job.VideoURL || got.Status != job.Status {
		t.Errorf("Get() = %+v, want %+v", got, job)
	}
}

func TestStore_GetUnknown(t *testing.T) {
	client := newTestRedis(t)
	store := worker.NewStore(client, time.Hour)

	_, err := store.Get(context.Background(), "does-not-exist")
	if !errors.Is(err, worker.ErrJobNotFound) {
		t.Errorf("Get() error = %v, want ErrJobNotFound", err)
	}
}

func TestStore_PutReplacesExisting(t *testing.T) {
	client := newTestRedis(t)
	store := worker.NewStore(client, time.Hour)

	if err := store.Put(context.Background(), worker.Job{ID: "abc", Status: worker.StatusQueued}); err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	if err := store.Put(context.Background(), worker.Job{ID: "abc", Status: worker.StatusCompleted}); err != nil {
		t.Fatalf("second Put() error = %v", err)
	}

	got, err := store.Get(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != worker.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, worker.StatusCompleted)
	}
}

func TestStore_TTLExpiry(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := worker.NewStore(client, 50*time.Millisecond)
	if err := store.Put(context.Background(), worker.Job{ID: "abc", Status: worker.StatusCompleted}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	if _, err := store.Get(context.Background(), "abc"); err != nil {
		t.Fatalf("Get() before TTL expiry error = %v, want nil", err)
	}

	mr.FastForward(100 * time.Millisecond)

	_, err = store.Get(context.Background(), "abc")
	if !errors.Is(err, worker.ErrJobNotFound) {
		t.Errorf("Get() after TTL expiry error = %v, want ErrJobNotFound", err)
	}
}

func TestStore_PutRefreshesTTL(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := worker.NewStore(client, 200*time.Millisecond)
	job := worker.Job{ID: "abc", Status: worker.StatusQueued}
	if err := store.Put(context.Background(), job); err != nil {
		t.Fatalf("first Put() error = %v", err)
	}

	mr.FastForward(150 * time.Millisecond) // still alive, but close to expiry

	job.Status = worker.StatusCompleted
	if err := store.Put(context.Background(), job); err != nil {
		t.Fatalf("second Put() error = %v", err)
	}

	mr.FastForward(150 * time.Millisecond) // would have expired if TTL wasn't refreshed

	got, err := store.Get(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Get() error = %v, want the TTL to have been refreshed by the second Put()", err)
	}
	if got.Status != worker.StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, worker.StatusCompleted)
	}
}

func TestStore_RedisUnavailable(t *testing.T) {
	client := unreachableRedis(t)
	store := worker.NewStore(client, time.Hour)

	_, err := store.Get(context.Background(), "abc")
	if !errors.Is(err, worker.ErrRedisUnavailable) {
		t.Errorf("Get() error = %v, want ErrRedisUnavailable", err)
	}

	err = store.Put(context.Background(), worker.Job{ID: "abc"})
	if !errors.Is(err, worker.ErrRedisUnavailable) {
		t.Errorf("Put() error = %v, want ErrRedisUnavailable", err)
	}
}
