package ratelimit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/auth"
	"github.com/vamshireddy02/mithyax/gateway/internal/ratelimit"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestRedis spins up an in-process fake Redis (miniredis) and
// returns both it and a real go-redis client connected to it — the
// same pattern internal/queue's tests use. The *miniredis.Miniredis
// handle itself is needed here (unlike in internal/queue) to prove the
// counter expires: TTL and FastForward, below.
func newTestRedis(t *testing.T) (*miniredis.Miniredis, *goredis.Client) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	t.Cleanup(mr.Close)

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return mr, client
}

// unreachableRedis returns a client pointed at an address nothing is
// listening on, for simulating Redis being unavailable — the same
// pattern internal/queue's tests use.
func unreachableRedis(t *testing.T) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { client.Close() })
	return client
}

// rateLimitKey mirrors Limiter.Allow's key format for tests that need
// to inspect the key miniredis actually stored — a small, deliberate
// duplication rather than exporting the format from the package for
// one test's sake.
func rateLimitKey(client string, windowStart time.Time) string {
	return fmt.Sprintf("rate_limit:%s:%s", client, windowStart.Format("2006-01-02T15:04"))
}

func TestLimiter_BelowLimit_Allowed(t *testing.T) {
	_, client := newTestRedis(t)
	limiter := ratelimit.New(client)

	for i := 1; i <= 5; i++ {
		result, err := limiter.Allow(context.Background(), "client-a", 5)
		if err != nil {
			t.Fatalf("Allow() call %d error = %v", i, err)
		}
		if !result.Allowed {
			t.Errorf("Allow() call %d = denied, want allowed (limit is 5)", i)
		}
	}
}

func TestLimiter_ExceedsLimit_Denied(t *testing.T) {
	_, client := newTestRedis(t)
	limiter := ratelimit.New(client)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if result, err := limiter.Allow(ctx, "client-a", 3); err != nil || !result.Allowed {
			t.Fatalf("Allow() call %d = (%+v, %v), want allowed", i, result, err)
		}
	}

	result, err := limiter.Allow(ctx, "client-a", 3)
	if err != nil {
		t.Fatalf("Allow() error = %v", err)
	}
	if result.Allowed {
		t.Error("Allow() on the 4th call over a limit of 3 = allowed, want denied")
	}
	if result.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want a positive duration", result.RetryAfter)
	}
	if result.RetryAfter > time.Minute {
		t.Errorf("RetryAfter = %v, want at most one window (1m)", result.RetryAfter)
	}
}

func TestLimiter_SeparateClients_IndependentLimits(t *testing.T) {
	_, client := newTestRedis(t)
	limiter := ratelimit.New(client)
	ctx := context.Background()

	// Exhaust client-a's limit of 2.
	for i := 0; i < 2; i++ {
		if result, err := limiter.Allow(ctx, "client-a", 2); err != nil || !result.Allowed {
			t.Fatalf("Allow(client-a) call %d = (%+v, %v), want allowed", i, result, err)
		}
	}
	if result, err := limiter.Allow(ctx, "client-a", 2); err != nil || result.Allowed {
		t.Fatalf("Allow(client-a) 3rd call = (%+v, %v), want denied", result, err)
	}

	// client-b has never made a request — its own limit is untouched.
	result, err := limiter.Allow(ctx, "client-b", 2)
	if err != nil {
		t.Fatalf("Allow(client-b) error = %v", err)
	}
	if !result.Allowed {
		t.Error("Allow(client-b) = denied, want allowed — a different client's exhausted limit must not affect this one")
	}
}

// TestLimiter_UserAndAdminIdentities_IndependentBuckets proves 7.7.3's
// explicit requirement at the level that actually matters in
// production: two distinct auth.Identity.ClientKey values (what
// Middleware — see middleware.go — actually passes as Allow's client
// argument) never share a counter, even though both may map to
// "the same two roles" conceptually.
func TestLimiter_UserAndAdminIdentities_IndependentBuckets(t *testing.T) {
	_, redisClient := newTestRedis(t)
	limiter := ratelimit.New(redisClient)
	ctx := context.Background()

	userIdentity := auth.Identity{Role: auth.RoleUser, ClientKey: "user-client-key"}
	adminIdentity := auth.Identity{Role: auth.RoleAdmin, ClientKey: "admin-client-key"}

	for i := 0; i < 2; i++ {
		if result, err := limiter.Allow(ctx, userIdentity.ClientKey, 2); err != nil || !result.Allowed {
			t.Fatalf("Allow(user) call %d = (%+v, %v), want allowed", i, result, err)
		}
	}
	if result, err := limiter.Allow(ctx, userIdentity.ClientKey, 2); err != nil || result.Allowed {
		t.Fatalf("Allow(user) 3rd call = (%+v, %v), want denied", result, err)
	}

	// The admin identity's bucket must be completely unaffected.
	result, err := limiter.Allow(ctx, adminIdentity.ClientKey, 2)
	if err != nil {
		t.Fatalf("Allow(admin) error = %v", err)
	}
	if !result.Allowed {
		t.Error("Allow(admin) = denied, want allowed — user and admin must not share a bucket")
	}
}

// TestLimiter_CounterExpires proves both halves of "the counter
// expires": the key Redis actually stores carries a TTL (not one that
// silently never gets set), and once that TTL elapses a fresh window
// starts a client back at zero rather than staying permanently
// exhausted.
func TestLimiter_CounterExpires(t *testing.T) {
	mr, redisClient := newTestRedis(t)

	current := time.Date(2026, 8, 29, 16, 30, 0, 0, time.UTC)
	limiter := ratelimit.New(redisClient, ratelimit.WithClock(func() time.Time { return current }))
	ctx := context.Background()

	windowStart := current.Truncate(time.Minute)
	key := rateLimitKey("client-a", windowStart)

	if result, err := limiter.Allow(ctx, "client-a", 1); err != nil || !result.Allowed {
		t.Fatalf("Allow() call 1 = (%+v, %v), want allowed", result, err)
	}

	ttl := mr.TTL(key)
	if ttl <= 0 {
		t.Fatalf("TTL(%q) = %v, want a positive TTL — the counter must expire on its own", key, ttl)
	}

	if result, err := limiter.Allow(ctx, "client-a", 1); err != nil || result.Allowed {
		t.Fatalf("Allow() call 2 (over the limit of 1) = (%+v, %v), want denied", result, err)
	}

	// Advance miniredis's own clock past the key's TTL and confirm Redis
	// actually reclaimed it, not just that a differently-named key would
	// eventually take over.
	mr.FastForward(ttl + time.Second)
	if mr.Exists(key) {
		t.Errorf("key %q still exists after its TTL elapsed", key)
	}

	// Move the fake clock into the next window — a fresh key, fresh count.
	current = current.Add(time.Minute)
	result, err := limiter.Allow(ctx, "client-a", 1)
	if err != nil {
		t.Fatalf("Allow() in the new window error = %v", err)
	}
	if !result.Allowed {
		t.Error("Allow() in a new window = denied, want allowed — the previous window's exhaustion must not carry over")
	}
}

// TestLimiter_RedisUnavailable_ReturnsDefinedError proves Redis
// failures aren't silently swallowed by Allow itself — they come back
// as ErrRedisUnavailable, letting a caller (see Middleware) decide
// what "defined behavior" means at the HTTP layer.
func TestLimiter_RedisUnavailable_ReturnsDefinedError(t *testing.T) {
	client := unreachableRedis(t)
	limiter := ratelimit.New(client)

	_, err := limiter.Allow(context.Background(), "client-a", 10)
	if !errors.Is(err, ratelimit.ErrRedisUnavailable) {
		t.Errorf("Allow() error = %v, want ErrRedisUnavailable", err)
	}
}

// TestLimiter_ConcurrentRequests_DoNotBypassLimit fires many concurrent
// Allow calls for one client against a small limit and proves exactly
// limit of them succeed — not more, which the atomic Lua increment
// (see incrementScript) exists specifically to guarantee under a race
// that a naive read-then-write counter would lose.
func TestLimiter_ConcurrentRequests_DoNotBypassLimit(t *testing.T) {
	_, redisClient := newTestRedis(t)
	limiter := ratelimit.New(redisClient)
	ctx := context.Background()

	const limit = 10
	const attempts = 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowedCount := 0

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := limiter.Allow(ctx, "client-a", limit)
			if err != nil {
				t.Errorf("Allow() error = %v", err)
				return
			}
			if result.Allowed {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowedCount != limit {
		t.Errorf("allowed %d of %d concurrent requests against a limit of %d, want exactly %d", allowedCount, attempts, limit, limit)
	}
}

// --- Middleware-level tests ---

func newLimiterRouter(t *testing.T, limiter *ratelimit.Limiter, scope string, limit int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		// Stands in for auth.Middleware: attaches a fixed Identity so
		// these tests exercise ratelimit.Middleware in isolation,
		// without needing a real token/config roundtrip — that's
		// covered by internal/auth's own tests and by
		// internal/httpserver's wiring test.
		auth.SetIdentity(c, auth.Identity{Role: auth.RoleUser, ClientKey: "test-client"})
		c.Next()
	})
	router.Use(ratelimit.Middleware(limiter, scope, limit, testLogger()))
	router.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func TestMiddleware_BelowLimit_Succeeds(t *testing.T) {
	_, redisClient := newTestRedis(t)
	limiter := ratelimit.New(redisClient)
	router := newLimiterRouter(t, limiter, "default", 3)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestMiddleware_ExceedsLimit_Returns429WithRetryAfter(t *testing.T) {
	_, redisClient := newTestRedis(t)
	limiter := ratelimit.New(redisClient)
	router := newLimiterRouter(t, limiter, "default", 1)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/protected", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", first.Code, http.StatusOK)
	}

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/protected", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want %d, body = %s", second.Code, http.StatusTooManyRequests, second.Body.String())
	}
	if second.Body.String() != `{"error":"rate limit exceeded"}` {
		t.Errorf("body = %s, want the fixed rate-limit-exceeded body", second.Body.String())
	}
	if retryAfter := second.Header().Get("Retry-After"); retryAfter == "" {
		t.Error("Retry-After header missing on a 429 response")
	}
}

// TestMiddleware_RedisUnavailable_FailsOpen proves the deliberate
// design choice documented on Middleware: a Redis outage lets
// requests through rather than blocking every authenticated caller.
func TestMiddleware_RedisUnavailable_FailsOpen(t *testing.T) {
	limiter := ratelimit.New(unreachableRedis(t))
	router := newLimiterRouter(t, limiter, "default", 1)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/protected", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d — Redis being unavailable must not block the request", rec.Code, http.StatusOK)
	}
}

// TestMiddleware_DifferentScopes_IndependentLimits proves the same
// client hitting two differently-scoped routes (e.g. the general API
// vs. POST /api/v1/analysis) is tracked by two separate counters —
// exhausting one must never affect the other.
func TestMiddleware_DifferentScopes_IndependentLimits(t *testing.T) {
	_, redisClient := newTestRedis(t)
	limiter := ratelimit.New(redisClient)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		auth.SetIdentity(c, auth.Identity{Role: auth.RoleUser, ClientKey: "test-client"})
		c.Next()
	})
	router.GET("/default", ratelimit.Middleware(limiter, "default", 1, testLogger()), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.POST("/analysis", ratelimit.Middleware(limiter, "analysis-create", 1, testLogger()), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	firstDefault := httptest.NewRecorder()
	router.ServeHTTP(firstDefault, httptest.NewRequest(http.MethodGet, "/default", nil))
	if firstDefault.Code != http.StatusOK {
		t.Fatalf("first /default request status = %d, want %d", firstDefault.Code, http.StatusOK)
	}
	secondDefault := httptest.NewRecorder()
	router.ServeHTTP(secondDefault, httptest.NewRequest(http.MethodGet, "/default", nil))
	if secondDefault.Code != http.StatusTooManyRequests {
		t.Fatalf("second /default request status = %d, want %d (limit of 1 exhausted)", secondDefault.Code, http.StatusTooManyRequests)
	}

	// /analysis's own limit for the same client must be untouched.
	analysisResp := httptest.NewRecorder()
	router.ServeHTTP(analysisResp, httptest.NewRequest(http.MethodPost, "/analysis", nil))
	if analysisResp.Code != http.StatusOK {
		t.Errorf("/analysis request status = %d, want %d — a different scope's limit must be independent", analysisResp.Code, http.StatusOK)
	}
}

// TestMiddleware_NoIdentity_FailsClosed proves Middleware refuses to
// rate-limit an anonymous bucket if it's ever wired without
// auth.Middleware ahead of it — a misconfiguration, not a state a real
// (already-authenticated) request can reach.
func TestMiddleware_NoIdentity_FailsClosed(t *testing.T) {
	_, redisClient := newTestRedis(t)
	limiter := ratelimit.New(redisClient)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ratelimit.Middleware(limiter, "default", 10, testLogger()))
	router.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/protected", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
