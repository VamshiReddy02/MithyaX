// Package auth provides authentication and authorization middleware
// for the gateway's HTTP API. Authentication (this file, 7.7.1) answers
// "who is this caller" via a shared bearer token; authorization
// (authorization.go, 7.7.2) answers "what is this caller allowed to
// do" given the role authentication attached to the request. The two
// stay deliberately separate: Middleware never looks at what route
// it's protecting, and RequireRole never looks at the token — it only
// ever reads the Identity Middleware already set.
//
// Kept intentionally simple: two fixed roles (user, admin), each
// backed by one static bearer token from configuration. No JWT,
// OAuth/OIDC, user database, RBAC tables, or multi-tenancy — those are
// later-phase concerns and nothing here should grow toward them.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Role is one of the gateway's two fixed authorization levels.
type Role string

const (
	// RoleUser is the baseline authenticated caller — every /api/v1
	// route except explicitly admin-only ones.
	RoleUser Role = "user"
	// RoleAdmin is a superset of RoleUser (see roleRank in
	// authorization.go): everything a user can do, plus admin-only
	// routes like GET /api/v1/sessions/metrics.
	RoleAdmin Role = "admin"
)

// Identity is what Middleware attaches to a successfully authenticated
// request's context — the "who" that RequireRole later checks the
// "what can they do" against, and (from Phase 7.7.3) what
// internal/ratelimit keys a client's request-count bucket on.
type Identity struct {
	Role Role
	// ClientKey identifies which configured token authenticated this
	// request, without being the token itself — see clientKey. Two
	// requests authenticated with different tokens always get different
	// ClientKeys (so, per 7.7.3, admin and user never share a rate-limit
	// bucket just because a naive key derived only from Role would
	// collapse them together); two requests with the same token always
	// get the same one.
	ClientKey string
}

// identityContextKey is the gin.Context key Middleware stores an
// Identity under. Unexported so nothing outside this package can set
// or overwrite it directly — SetIdentity/IdentityFromContext are the
// only way in or out.
const identityContextKey = "auth.identity"

// SetIdentity attaches identity to c. Exported for tests that need to
// exercise RequireRole in isolation, without going through Middleware
// first.
func SetIdentity(c *gin.Context, identity Identity) {
	c.Set(identityContextKey, identity)
}

// IdentityFromContext returns the Identity Middleware attached to c,
// if any. ok is false if Middleware never ran on this request (a
// misconfigured route) or hasn't reached this point in the chain yet.
func IdentityFromContext(c *gin.Context) (Identity, bool) {
	value, exists := c.Get(identityContextKey)
	if !exists {
		return Identity{}, false
	}
	identity, ok := value.(Identity)
	return identity, ok
}

// bearerPrefix is the required Authorization header scheme. Comparison
// against it is case-sensitive, per RFC 6750 — "bearer" or "BEARER" is
// a malformed header, not a valid one spelled differently.
const bearerPrefix = "Bearer "

// unauthorizedBody is the single, fixed response for every
// authentication rejection (missing header, wrong scheme, wrong
// token). It deliberately never echoes back anything the caller sent,
// so a token typo can never be partially confirmed or denied from the
// response alone.
var unauthorizedBody = gin.H{"error": "unauthorized"}

// Middleware returns Gin middleware that requires every request to
// carry "Authorization: Bearer <token>" where token is a key of
// tokens, rejecting anything else with 401 before the request reaches
// a handler. On success it attaches an Identity carrying the matched
// token's Role (see SetIdentity) — RequireRole (authorization.go)
// reads that to decide access; the handler never sees the token
// itself.
//
// tokens is expected to be built from GATEWAY_AUTH_TOKEN /
// GATEWAY_ADMIN_AUTH_TOKEN (see internal/config), neither of which has
// a default: an auth check with a built-in fallback value isn't a real
// check. Mapping a fixed set of static tokens to roles is
// intentionally the whole mechanism for now — see the package doc for
// why this doesn't grow into per-caller tokens or JWT-carried claims.
func Middleware(tokens map[string]Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		provided, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok || provided == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, unauthorizedBody)
			return
		}

		role, matched := matchToken(provided, tokens)
		if !matched {
			c.AbortWithStatusJSON(http.StatusUnauthorized, unauthorizedBody)
			return
		}

		SetIdentity(c, Identity{Role: role, ClientKey: clientKey(provided)})
		c.Next()
	}
}

// clientKey derives a stable, non-reversible identifier from an
// authenticated token — for use anywhere (see internal/ratelimit) that
// needs to key state per-caller without ever storing or transmitting
// the raw credential itself, e.g. as part of a Redis key name where it
// could otherwise show up in KEYS, MONITOR, or a slow-query log. A
// truncated SHA-256 hex digest is already effectively collision-free
// for the small, fixed set of tokens this phase configures.
func clientKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

// matchToken compares provided against every key of tokens in constant
// time, returning the first match's role. Every candidate is compared
// (rather than stopping at the first — there normally are only two)
// so a genuine match's position among the configured tokens can't be
// inferred from timing either.
func matchToken(provided string, tokens map[string]Role) (Role, bool) {
	var matchedRole Role
	found := false
	for token, role := range tokens {
		if tokensEqual(provided, token) {
			matchedRole = role
			found = true
		}
	}
	return matchedRole, found
}

// bearerToken extracts the token from a "Bearer <token>" Authorization
// header value. ok is false for any other scheme (including a bare
// "Bearer" with nothing after it, which doesn't even have the required
// separating space).
func bearerToken(header string) (token string, ok bool) {
	if !strings.HasPrefix(header, bearerPrefix) {
		return "", false
	}
	return strings.TrimPrefix(header, bearerPrefix), true
}

// tokensEqual compares in constant time so a mismatch's timing can't
// be used to guess a configured token one byte at a time.
// ConstantTimeCompare only compares equal-length inputs meaningfully,
// so a length mismatch is checked (and rejected) separately first.
func tokensEqual(provided, configured string) bool {
	if len(provided) != len(configured) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) == 1
}
