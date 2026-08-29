// Package ratelimit provides Redis-backed rate limiting for the
// gateway's HTTP API (Phase 7.7.3) — the last stage before a request
// reaches its handler:
//
//	Client -> auth.Middleware -> auth.RequireRole -> ratelimit.Middleware -> handler
//
// Redis (rather than an in-memory counter) is deliberate: MithyaX may
// eventually run multiple gateway replicas behind a load balancer, and
// an in-memory limiter would let one client get a separate quota per
// replica just by being routed around — a shared Redis counter keeps
// the limit meaningful regardless of how many gateway processes are
// running.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/auth"
)

// ErrRedisUnavailable wraps any Redis failure encountered while
// checking or updating a client's counter.
var ErrRedisUnavailable = errors.New("rate limiter: redis is unavailable")

// window is the fixed bucket size every limit in this phase is
// measured over (7.7.3: "a fixed one-minute window") — not
// configurable per call, since every limit the gateway defines today
// is a per-minute limit; a real per-route window would need the key
// format below to carry more than minute resolution.
const window = time.Minute

// ttl gives an expiring key a little more than one window's worth of
// life. The exact margin doesn't matter — a window's identity lives
// entirely in its key name (the truncated, formatted timestamp), not
// in when Redis happens to reclaim it — it only needs to comfortably
// outlast the window it counts.
const ttl = 2 * time.Minute

// incrementScript atomically increments the window's counter and, the
// first time this key is touched (current == 1), sets it to expire.
// Doing this as one Lua script (one Redis round trip, executed
// atomically by Redis's single-threaded command processing) rather
// than a separate INCR then EXPIRE closes two gaps a naive two-command
// version would have: two concurrent requests racing on a brand-new
// key can't each decide "I'm the first" and both try to set the TTL
// redundantly-but-harmlessly (that part would've been fine), and more
// importantly a crash or timeout between the two commands can never
// leave a key permanently without a TTL.
const incrementScript = `
local current = redis.call("INCR", KEYS[1])
if current == 1 then
    redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return current
`

// Limiter enforces per-client, per-minute request limits backed by
// Redis. One Limiter is shared by every rate-limited route; what
// varies per route is the requests-per-minute ceiling and a scope
// string keeping one route's counter for a client separate from
// another route's counter for that same client (see Middleware).
type Limiter struct {
	client *goredis.Client
	script *goredis.Script
	clock  func() time.Time
}

// Option configures a Limiter built by New.
type Option func(*Limiter)

// WithClock overrides the clock Allow uses to compute the current
// window, for tests that need to cross a window boundary without
// sleeping a real minute.
func WithClock(clock func() time.Time) Option {
	return func(l *Limiter) { l.clock = clock }
}

// New builds a Limiter backed by client — the shared gateway Redis
// connection (see internal/redis.Client).
func New(client *goredis.Client, opts ...Option) *Limiter {
	l := &Limiter{
		client: client,
		script: goredis.NewScript(incrementScript),
		clock:  func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Result is the outcome of one Allow check.
type Result struct {
	// Allowed reports whether this request may proceed.
	Allowed bool
	// RetryAfter is how long the client should wait before its next
	// attempt is likely to succeed. Only meaningful when !Allowed.
	RetryAfter time.Duration
}

// Allow atomically increments client's counter for the current
// one-minute window and reports whether the resulting count is still
// within limit.
//
// client is expected to be a stable, non-secret identifier for the
// caller — see auth.Identity.ClientKey, a value derived from (not
// equal to) the caller's bearer token, since the raw token must never
// end up embedded in a Redis key name (visible via KEYS/MONITOR/slow
// log). The key itself is rate_limit:{client}:{window}, e.g.
// rate_limit:a1b2c3d4:2026-08-29T16:30.
func (l *Limiter) Allow(ctx context.Context, client string, limit int) (Result, error) {
	now := l.clock()
	windowStart := now.Truncate(window)
	key := fmt.Sprintf("rate_limit:%s:%s", client, windowStart.Format("2006-01-02T15:04"))

	count, err := l.script.Run(ctx, l.client, []string{key}, ttl.Milliseconds()).Int64()
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}

	if count > int64(limit) {
		retryAfter := windowStart.Add(window).Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		return Result{Allowed: false, RetryAfter: retryAfter}, nil
	}
	return Result{Allowed: true}, nil
}

// tooManyRequestsBody is the fixed 429 response body (7.7.3 — kept
// deliberately simple; retry timing goes in the Retry-After header,
// not duplicated into the body).
var tooManyRequestsBody = gin.H{"error": "rate limit exceeded"}

// Middleware returns Gin middleware enforcing limit requests per
// minute per authenticated client, under scope (so this route's limit
// for a client never shares a counter with a different route's limit
// for that same client — see Allow).
//
// It must run after auth.Middleware in the chain: it reads the
// Identity auth.Middleware already attached to the request rather than
// looking at the Authorization header itself, which is what makes
// "unauthenticated requests get 401, never 429" automatic — a request
// with no valid token is already rejected by auth.Middleware and never
// reaches here at all.
//
// A Redis failure fails open (the request proceeds) rather than
// closed: rate limiting defends against abuse, it isn't a correctness
// guarantee the rest of the API depends on, and making every
// authenticated request fail whenever Redis has a blip would turn a
// secondary protection mechanism into a second outage on top of
// whatever's already wrong with Redis. The failure is logged, not
// silent.
func Middleware(limiter *Limiter, scope string, limit int, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := auth.IdentityFromContext(c)
		if !ok {
			// Only reachable if this middleware is wired without (or
			// ahead of) auth.Middleware — a routing bug, not something a
			// real client can trigger. Failing loudly here rather than
			// silently rate-limiting some anonymous shared bucket makes
			// that bug obvious instead of quietly wrong.
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "rate limiter misconfigured: no authenticated identity"})
			return
		}

		result, err := limiter.Allow(c.Request.Context(), identity.ClientKey+":"+scope, limit)
		if err != nil {
			logger.Warn("rate limiter unavailable, allowing request",
				slog.String("scope", scope), slog.String("error", err.Error()))
			c.Next()
			return
		}

		if !result.Allowed {
			retrySeconds := int(result.RetryAfter.Round(time.Second) / time.Second)
			if retrySeconds < 1 {
				retrySeconds = 1
			}
			c.Header("Retry-After", strconv.Itoa(retrySeconds))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, tooManyRequestsBody)
			return
		}

		c.Next()
	}
}
