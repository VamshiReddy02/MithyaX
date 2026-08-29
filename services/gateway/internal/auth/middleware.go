// Package auth provides authentication middleware for the gateway's
// HTTP API. Phase 7.7.1 is authentication only — establishing "is this
// caller who they claim to be" via a single shared bearer token.
// Authorization ("is this caller allowed to do this"), rate limiting,
// and request idempotency are deliberately out of scope here and
// belong to later phases; nothing in this package should grow toward
// any of them.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// bearerPrefix is the required Authorization header scheme. Comparison
// against it is case-sensitive, per RFC 6750 — "bearer" or "BEARER" is
// a malformed header, not a valid one spelled differently.
const bearerPrefix = "Bearer "

// unauthorizedBody is the single, fixed response for every rejection
// path (missing header, wrong scheme, wrong token). It deliberately
// never echoes back anything the caller sent, so a token typo can
// never be partially confirmed or denied from the response alone.
var unauthorizedBody = gin.H{"error": "unauthorized"}

// Middleware returns Gin middleware that requires every request to
// carry "Authorization: Bearer <token>" matching token exactly,
// rejecting anything else with 401 before the request reaches a
// handler. Handlers never see or check the token themselves — that
// would scatter this check across every handler and risk one being
// added later that forgets it; putting it in middleware makes "does
// this route require auth" a routing decision (see httpserver.New),
// not a per-handler one.
//
// token is expected to come from GATEWAY_AUTH_TOKEN (see
// internal/config), which has no default: an auth check with a
// built-in fallback value isn't a real check. A static shared token is
// intentionally the whole mechanism for now — see the package doc for
// why this stays a single comparison rather than growing into
// per-caller tokens, scopes, or JWT.
func Middleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		provided, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok || provided == "" || !tokensEqual(provided, token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, unauthorizedBody)
			return
		}
		c.Next()
	}
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
// be used to guess the configured token one byte at a time.
// ConstantTimeCompare only compares equal-length inputs meaningfully,
// so a length mismatch is checked (and rejected) separately first.
func tokensEqual(provided, configured string) bool {
	if len(provided) != len(configured) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) == 1
}
