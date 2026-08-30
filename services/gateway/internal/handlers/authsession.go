package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/sessioncred"
)

// SessionCredentialIssuer mints short-lived session credentials.
// *sessioncred.Store implements it.
type SessionCredentialIssuer interface {
	Issue(ctx context.Context) (sessioncred.Credential, error)
}

// NewAuthSession builds the POST /api/v1/auth/session handler (Phase
// 8.1): guarded by auth.ExtensionMiddleware, not the general
// auth.Middleware/RequireRole chain the rest of /api/v1 uses — see that
// middleware's own doc for why. It mints one short-lived credential and
// returns it; the caller (the Chrome extension) then presents that
// credential, not GATEWAY_EXTENSION_TOKEN itself, to actually create
// and connect to a live session (POST /api/v1/sessions,
// GET /api/v1/sessions/ws).
func NewAuthSession(issuer SessionCredentialIssuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		cred, err := issuer.Issue(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "failed to issue session credential"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"credential": cred.Token,
			"expires_at": cred.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
}
