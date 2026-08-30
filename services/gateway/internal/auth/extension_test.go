package auth_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/auth"
)

const testExtensionToken = "test-extension-development-token"

// newExtensionRouter builds a router with only ExtensionMiddleware,
// mirroring newAuthRouter (middleware_test.go) for the extension's own,
// separate credential.
func newExtensionRouter(token string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/auth/session", auth.ExtensionMiddleware(token), func(c *gin.Context) {
		identity, ok := auth.IdentityFromContext(c)
		c.JSON(http.StatusOK, gin.H{"ok": true, "identity_set": ok, "role": string(identity.Role)})
	})
	return router
}

func TestExtensionMiddleware_MissingToken(t *testing.T) {
	router := newExtensionRouter(testExtensionToken)
	rec := doRequest(t, router, "/auth/session", "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestExtensionMiddleware_WrongToken(t *testing.T) {
	router := newExtensionRouter(testExtensionToken)
	rec := doRequest(t, router, "/auth/session", "Bearer wrong-token")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// TestExtensionMiddleware_UserOrAdminTokenRejected proves the whole
// point of having a separate extension token: the gateway's normal
// long-lived tokens must NOT satisfy this middleware — if they did,
// anything already trusted with a user/admin token would gain nothing
// extra by using the extension's flow, but conversely a route meant
// only for the extension token would also start accepting the far more
// powerful tokens, which is the opposite of minimum privilege.
func TestExtensionMiddleware_UserOrAdminTokenRejected(t *testing.T) {
	router := newExtensionRouter(testExtensionToken)

	for _, tok := range []string{testToken, testAdminToken} {
		rec := doRequest(t, router, "/auth/session", "Bearer "+tok)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want %d, body = %s", tok, rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	}
}

func TestExtensionMiddleware_MalformedHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"basic scheme", "Basic dXNlcjpwYXNz"},
		{"bearer with no token", "Bearer"},
		{"bearer with trailing space only", "Bearer "},
		{"lowercase scheme", "bearer " + testExtensionToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newExtensionRouter(testExtensionToken)
			rec := doRequest(t, router, "/auth/session", tt.header)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}

func TestExtensionMiddleware_ValidToken(t *testing.T) {
	router := newExtensionRouter(testExtensionToken)
	rec := doRequest(t, router, "/auth/session", "Bearer "+testExtensionToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != `{"identity_set":true,"ok":true,"role":"extension"}` {
		t.Errorf("body = %s, want an Identity with Role=extension attached", rec.Body.String())
	}
}
