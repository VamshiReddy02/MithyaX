package redis_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/redis"
)

func newTestClient(t *testing.T) *redis.Client {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	t.Cleanup(mr.Close)

	client, err := redis.New("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("redis.New() error = %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return client
}

func TestNew_InvalidURL(t *testing.T) {
	if _, err := redis.New("not a redis url"); err == nil {
		t.Fatal("New() with an invalid URL: expected error, got nil")
	}
}

func TestNew_DoesNotDialEagerly(t *testing.T) {
	// A syntactically valid URL pointed at nothing listening should still
	// construct successfully — like pgxpool.New, connection failures only
	// surface once something actually uses the client (see
	// TestHealthCheck_Unreachable).
	client, err := redis.New("redis://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New() error = %v, want nil (no eager dial)", err)
	}
	defer client.Close()
}

func TestHealthCheck_Reachable(t *testing.T) {
	client := newTestClient(t)

	if err := client.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck() error = %v, want nil", err)
	}
}

func TestHealthCheck_Unreachable(t *testing.T) {
	client, err := redis.New("redis://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := client.HealthCheck(ctx); err == nil {
		t.Error("HealthCheck() error = nil, want an error for an unreachable Redis")
	}
}

// TestClient_SetGetDel proves the wrapper's embedding actually exposes
// go-redis's own commands — Client is meant to be usable everywhere a
// plain *goredis.Client already is (see internal/worker).
func TestClient_SetGetDel(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	if err := client.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, err := client.Get(ctx, "k").Result()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != "v" {
		t.Errorf("Get() = %q, want %q", got, "v")
	}

	if err := client.Del(ctx, "k").Err(); err != nil {
		t.Fatalf("Del() error = %v", err)
	}

	_, err = client.Get(ctx, "k").Result()
	if !errors.Is(err, goredis.Nil) {
		t.Errorf("Get() after Del() error = %v, want redis.Nil", err)
	}
}

// TestClient_ReconnectsAfterTransientFailure proves a client built once
// keeps working across a connection blip — go-redis pools connections
// and redials on demand rather than holding one connection for the
// client's lifetime, so a transient outage shouldn't need a whole new
// Client to recover, only for the outage to end.
func TestClient_ReconnectsAfterTransientFailure(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	client, err := redis.New("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("redis.New() error = %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Set(ctx, "k", "before", 0).Err(); err != nil {
		t.Fatalf("Set() before outage error = %v", err)
	}

	// Simulate a transient outage: stop the server (severing any pooled
	// connections), confirm the client surfaces a clear error rather
	// than hanging, then bring it back up on the same port.
	mr.Close()
	if _, err := client.Get(ctx, "k").Result(); err == nil {
		t.Fatal("Get() during outage: expected an error, got nil")
	}

	if err := mr.Restart(); err != nil {
		t.Fatalf("mr.Restart() error = %v", err)
	}

	if err := client.Set(ctx, "k", "after", 0).Err(); err != nil {
		t.Fatalf("Set() after restart error = %v, want the client to have recovered without being rebuilt", err)
	}
	got, err := client.Get(ctx, "k").Result()
	if err != nil || got != "after" {
		t.Errorf("Get() after restart = (%q, %v), want (\"after\", nil)", got, err)
	}
}

// TestGatewayTestRedisURL exercises the same client against a real,
// separately-running Redis instance named by GATEWAY_TEST_REDIS_URL,
// skipping (not failing) if that isn't set — the live-instance half of
// 7.3.4, mirroring how internal/database's tests skip without a real
// Postgres. There's deliberately no hardcoded fallback URL: Redis here
// has no auth configured, so this isn't a credential-secrecy concern
// the way GATEWAY_TEST_DATABASE_URL is, but the address itself is still
// environment-specific and doesn't belong assumed in source.
func TestGatewayTestRedisURL(t *testing.T) {
	testRedisURL := os.Getenv("GATEWAY_TEST_REDIS_URL")
	if testRedisURL == "" {
		t.Skip("GATEWAY_TEST_REDIS_URL not set; skipping test that needs a real Redis instance (see deployments/docker/docker-compose.yml)")
	}

	client, err := redis.New(testRedisURL)
	if err != nil {
		t.Fatalf("redis.New() error = %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.HealthCheck(ctx); err != nil {
		t.Skipf("Redis not reachable at %s: %v", testRedisURL, err)
	}

	key := "mithyax:test:client"
	if err := client.Set(ctx, key, "hello", 0).Err(); err != nil {
		t.Fatalf("SET error = %v", err)
	}
	got, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	if got != "hello" {
		t.Errorf("GET = %q, want %q", got, "hello")
	}
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatalf("DEL error = %v", err)
	}
	if _, err := client.Get(ctx, key).Result(); !errors.Is(err, goredis.Nil) {
		t.Errorf("GET after DEL error = %v, want redis.Nil", err)
	}
}
