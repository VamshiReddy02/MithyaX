// Package sessioncred issues and validates the short-lived credential
// the Chrome extension exchanges for at POST /api/v1/auth/session
// (Phase 8.1) — the whole point of which is that the extension never
// holds GATEWAY_AUTH_TOKEN or GATEWAY_ADMIN_AUTH_TOKEN, the gateway's
// long-lived service credentials. Instead it holds only
// GATEWAY_EXTENSION_TOKEN (see auth.ExtensionMiddleware), which
// authorizes nothing but minting one of these: an opaque, unguessable,
// time-bounded credential good for exactly two routes,
// POST /api/v1/sessions and GET /api/v1/sessions/ws (see
// internal/httpserver's session-auth wiring) — nothing else in the API
// ever accepts one. A leaked extension token only lets an attacker mint
// more of these narrowly-scoped, short-lived credentials; a leaked
// session credential itself expires on its own within minutes. Neither
// comes close to the blast radius of a leaked AuthToken/AdminAuthToken.
package sessioncred

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrRedisUnavailable wraps a Redis connectivity failure encountered
// while issuing or validating a credential.
var ErrRedisUnavailable = errors.New("redis is unavailable")

// credentialKeyPrefix namespaces this package's keys in Redis,
// mirroring internal/worker.Store's jobKeyPrefix.
const credentialKeyPrefix = "mithyax:extsession:"

// Credential is what POST /api/v1/auth/session hands back to the
// extension: Token is the opaque bearer value it must present (as an
// Authorization header, or the "credential" query parameter on the
// WebSocket route a browser can't attach a header to) on every
// subsequent request to the two routes it's scoped to; ExpiresAt is
// informational, so the extension knows when to call
// POST /api/v1/auth/session again rather than discovering it's expired
// from a suddenly-failing request.
type Credential struct {
	Token     string
	ExpiresAt time.Time
}

// Store issues and validates session credentials, backed by Redis so
// validation works the same way regardless of which gateway replica
// issued a given credential. Safe for concurrent use — like
// internal/worker.Store, it's a thin wrapper over the (already
// concurrency-safe) Redis client.
type Store struct {
	redis *redis.Client
	ttl   time.Duration
}

// NewStore builds a Store backed by client. Every credential Issue
// mints is valid for ttl from that moment — see
// config.ExtensionSessionCredentialTTL.
func NewStore(client *redis.Client, ttl time.Duration) *Store {
	return &Store{redis: client, ttl: ttl}
}

// Issue mints a new, random Credential and records it in Redis with
// this Store's TTL. Not tied to any particular session_id — the
// extension calls this before it has one (see the 8.1 design's
// ordering: authenticate first, then create a session) — so it's valid
// for creating and connecting to whatever sessions the extension opens
// until it expires, not scoped to a single one. Not single-use either:
// a short TTL already bounds a leaked credential's exposure, and
// making it single-use would break a browser reconnect within that
// window for no real security gain — reconnect handling is deliberately
// out of scope for this phase regardless (see internal/httpserver).
func (s *Store) Issue(ctx context.Context) (Credential, error) {
	token, err := newCredentialToken()
	if err != nil {
		return Credential{}, fmt.Errorf("generate credential: %w", err)
	}

	expiresAt := time.Now().Add(s.ttl)
	if err := s.redis.Set(ctx, credentialKeyPrefix+credentialKey(token), "1", s.ttl).Err(); err != nil {
		return Credential{}, fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}

	return Credential{Token: token, ExpiresAt: expiresAt}, nil
}

// Validate reports whether token is a currently-live credential this
// Store issued — false (with a nil error) for one that never existed,
// already expired, or was never issued at all, exactly like a wrong
// long-lived token being rejected: the caller shouldn't be able to
// distinguish those cases from the response alone.
func (s *Store) Validate(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}

	n, err := s.redis.Exists(ctx, credentialKeyPrefix+credentialKey(token)).Result()
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return n > 0, nil
}

// newCredentialToken generates a random, high-entropy opaque token —
// 256 bits, well beyond what's guessable even across this credential's
// entire (short) lifetime.
func newCredentialToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// credentialKey derives a Redis key suffix from a credential's token
// without ever storing or transmitting the raw token as a key name
// itself — mirrors auth.clientKey's identical rationale: a raw secret
// showing up in Redis's own KEYS, MONITOR, or slow-query log would be
// its own, separate leak.
func credentialKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
