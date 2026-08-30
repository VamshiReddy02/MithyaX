package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/sessioncred"
)

type fakeSessionCredentialIssuer struct {
	cred sessioncred.Credential
	err  error
}

func (f *fakeSessionCredentialIssuer) Issue(ctx context.Context) (sessioncred.Credential, error) {
	if f.err != nil {
		return sessioncred.Credential{}, f.err
	}
	return f.cred, nil
}

func newAuthSessionRouter(issuer handlers.SessionCredentialIssuer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/auth/session", handlers.NewAuthSession(issuer))
	return router
}

func TestAuthSession_Success(t *testing.T) {
	expiresAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	issuer := &fakeSessionCredentialIssuer{cred: sessioncred.Credential{Token: "opaque-token-value", ExpiresAt: expiresAt}}
	router := newAuthSessionRouter(issuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var body struct {
		Credential string `json:"credential"`
		ExpiresAt  string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Credential != "opaque-token-value" {
		t.Errorf("credential = %q, want %q", body.Credential, "opaque-token-value")
	}
	if body.ExpiresAt != "2026-01-01T00:00:00Z" {
		t.Errorf("expires_at = %q, want %q", body.ExpiresAt, "2026-01-01T00:00:00Z")
	}
}

func TestAuthSession_IssuerFailure(t *testing.T) {
	issuer := &fakeSessionCredentialIssuer{err: errors.New("redis is unavailable")}
	router := newAuthSessionRouter(issuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

// TestAuthSession_NeverEchoesExtensionToken is a documentation-style
// guard: the response body must contain only the minted credential and
// its expiry, never anything resembling the extension token the caller
// authenticated this request with (auth.ExtensionMiddleware runs
// upstream of this handler in production — this handler itself never
// even sees it, but the response shape is asserted here explicitly).
func TestAuthSession_NeverEchoesExtensionToken(t *testing.T) {
	issuer := &fakeSessionCredentialIssuer{cred: sessioncred.Credential{Token: "opaque-token-value", ExpiresAt: time.Now()}}
	router := newAuthSessionRouter(issuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 2 {
		t.Errorf("response body has %d fields, want exactly 2 (credential, expires_at): %v", len(body), body)
	}
}
