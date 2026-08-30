package sessioncred_test

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedis mirrors internal/worker's identical helper — a fresh,
// isolated miniredis instance per test.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return client
}

// unreachableRedis mirrors internal/worker's identical helper — a
// client pointed at an address nothing is listening on, for simulating
// Redis being unavailable.
func unreachableRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { client.Close() })
	return client
}
