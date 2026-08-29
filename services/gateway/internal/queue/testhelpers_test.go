package queue_test

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// newTestRedis spins up an in-process fake Redis (miniredis) and returns
// a real go-redis client connected to it — the same pattern
// internal/worker's tests use, so internal/queue's Redis implementation
// is exercised against real wire protocol and real BLPOP/LLEN semantics
// without an external process.
func newTestRedis(t *testing.T) *goredis.Client {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	t.Cleanup(mr.Close)

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return client
}

// unreachableRedis returns a client pointed at an address nothing is
// listening on, for simulating Redis being unavailable.
func unreachableRedis(t *testing.T) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1, // fail fast in tests instead of go-redis's default retry-with-backoff
	})
	t.Cleanup(func() { client.Close() })
	return client
}
