package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/auth"
	"github.com/vamshireddy02/mithyax/gateway/internal/sessioncred"
)

// sessionBearerPrefix mirrors auth.Middleware's identical constant —
// duplicated rather than exported from internal/auth (see this file's
// own doc for why this whole check stays independent of that package).
const sessionBearerPrefix = "Bearer "

// sessionAuth authenticates POST /api/v1/sessions and
// GET /api/v1/sessions/ws (Phase 8.1) — the two routes a short-lived
// session credential from POST /api/v1/auth/session is scoped to, and
// nothing else. It accepts either of:
//
//   - One of the gateway's normal long-lived tokens (tokens), checked
//     exactly the way auth.Middleware already checks them everywhere
//     else in the API — every existing caller of these two routes
//     keeps working unchanged.
//   - A session credential (credStore), presented as an
//     "Authorization: Bearer <credential>" header (POST /sessions is an
//     ordinary fetch, which can set one) or a "credential" query
//     parameter (GET /sessions/ws is a WebSocket upgrade — a browser's
//     WebSocket API can't attach a header to it, the same reason
//     session_id itself is already a query parameter here).
//
// Deliberately its own, self-contained check rather than a change to
// auth.Middleware itself: that middleware's tokens map is static
// configuration fixed at startup, while a session credential is
// dynamic, per-request state validated against Redis — bolting that
// onto Middleware would change what every other /api/v1 route's
// existing, already-relied-on auth check does. This file adds a
// capability; it doesn't touch that one.
func sessionAuth(tokens map[string]auth.Role, credStore *sessioncred.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if provided, ok := sessionBearerToken(c.GetHeader("Authorization")); ok && provided != "" {
			if role, matched := matchStaticToken(provided, tokens); matched {
				auth.SetIdentity(c, auth.Identity{Role: role, ClientKey: sessionClientKey(provided)})
				c.Next()
				return
			}

			if valid, err := credStore.Validate(c.Request.Context(), provided); err == nil && valid {
				auth.SetIdentity(c, auth.Identity{Role: auth.RoleUser, ClientKey: sessionClientKey(provided)})
				c.Next()
				return
			}
		}

		if provided := c.Query("credential"); provided != "" {
			if valid, err := credStore.Validate(c.Request.Context(), provided); err == nil && valid {
				auth.SetIdentity(c, auth.Identity{Role: auth.RoleUser, ClientKey: sessionClientKey(provided)})
				c.Next()
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	}
}

// sessionBearerToken mirrors auth's identical, unexported helper.
func sessionBearerToken(header string) (token string, ok bool) {
	if !strings.HasPrefix(header, sessionBearerPrefix) {
		return "", false
	}
	return strings.TrimPrefix(header, sessionBearerPrefix), true
}

// matchStaticToken mirrors auth.Middleware's own matching logic
// (constant-time, every candidate compared) against the same tokens map
// httpserver.New builds from GATEWAY_AUTH_TOKEN/GATEWAY_ADMIN_AUTH_TOKEN.
func matchStaticToken(provided string, tokens map[string]auth.Role) (auth.Role, bool) {
	var matchedRole auth.Role
	found := false
	for token, role := range tokens {
		if len(provided) != len(token) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1 {
			matchedRole = role
			found = true
		}
	}
	return matchedRole, found
}

// sessionClientKey mirrors auth's identical clientKey helper — a
// stable, non-reversible per-credential identifier for
// ratelimit.Middleware to key on, so a session credential never shares
// a rate-limit bucket with an unrelated one purely because both hashed
// to the same key would otherwise be a bug, not because of how this is
// derived.
func sessionClientKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}
