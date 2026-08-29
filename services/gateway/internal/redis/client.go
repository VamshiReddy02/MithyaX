// Package redis owns the gateway's Redis connection: construction from
// a URL, its lifecycle (Close), and a health check — the same role
// internal/database plays for PostgreSQL, kept as a separate package
// because the two serve unrelated purposes (durable persistence vs.
// coordination/queues) and shouldn't be coupled just because they're
// both "a database".
package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// Client wraps a go-redis client. Embedding it (rather than naming the
// field, the way database.DB names its pool "Pool") means callers that
// only need it to behave like a plain Redis client — the existing
// internal/worker package, for one — can keep working against
// *goredis.Client unchanged by reaching through the promoted field
// (client.Client), while call sites that care about the gateway's own
// lifecycle/health-check story use the wrapper.
type Client struct {
	*goredis.Client
}

// New parses redisURL and builds a Client from it. Like pgxpool.New,
// this only validates the URL and prepares the client — it doesn't
// dial eagerly, so a syntactically valid but unreachable URL only
// surfaces once something actually uses the connection. Call
// HealthCheck to verify reachability explicitly (e.g. at startup, or
// from the health endpoint).
func New(redisURL string) (*Client, error) {
	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Client{Client: goredis.NewClient(opts)}, nil
}

// HealthCheck reports whether Redis is currently reachable.
func (c *Client) HealthCheck(ctx context.Context) error {
	return c.Ping(ctx).Err()
}
