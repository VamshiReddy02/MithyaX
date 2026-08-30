package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RoleExtension identifies the Chrome extension's own, separate
// credential (Phase 8.1) — deliberately absent from roleRank
// (authorization.go): it isn't a weaker version of RoleUser that
// RequireRole ranks below it, it's authorized for exactly one route,
// the one ExtensionMiddleware below guards, and RequireRole is never
// applied to that route. It exists as a real Role value only so
// ExtensionMiddleware has something to attach via SetIdentity —
// ratelimit.Middleware (7.7.3) requires every authenticated request to
// carry an Identity, this route included.
const RoleExtension Role = "extension"

// ExtensionMiddleware returns Gin middleware guarding exactly one
// route: POST /api/v1/auth/session (Phase 8.1). It checks a single
// token — GATEWAY_EXTENSION_TOKEN — entirely independent of
// Middleware's tokens map: adding this token there instead would
// silently authorize it for every other /api/v1 route neither
// Middleware nor RequireRole currently restricts, defeating the reason
// this token exists at all. The Chrome extension holds only this
// token, never AuthToken or AdminAuthToken, and exchanges it here for a
// short-lived session credential (see internal/sessioncred) that is
// the only thing it actually uses to reach the rest of the API.
func ExtensionMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		provided, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok || provided == "" || !tokensEqual(provided, token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, unauthorizedBody)
			return
		}

		SetIdentity(c, Identity{Role: RoleExtension, ClientKey: clientKey(provided)})
		c.Next()
	}
}
