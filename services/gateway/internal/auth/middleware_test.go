package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/auth"
)

const (
	testToken      = "test-development-token"
	testAdminToken = "test-admin-development-token"
)

// testTokens is the token->role mapping every test in this file uses
// unless it needs something different — mirrors how httpserver.New
// actually builds this map from GATEWAY_AUTH_TOKEN/
// GATEWAY_ADMIN_AUTH_TOKEN.
func testTokens() map[string]auth.Role {
	return map[string]auth.Role{
		testToken:      auth.RoleUser,
		testAdminToken: auth.RoleAdmin,
	}
}

// newAuthRouter builds a router with only Middleware — for tests of
// authentication itself (401 vs. reaching the handler), independent of
// any authorization decision.
func newAuthRouter(tokens map[string]auth.Role) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(auth.Middleware(tokens))
	router.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

// newRoleRouter builds a router with Middleware plus one route gated
// by RequireRole(auth.RoleAdmin) — for tests of authorization (403 vs.
// reaching the handler) layered on top of already-successful
// authentication.
func newRoleRouter(tokens map[string]auth.Role) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(auth.Middleware(tokens))
	router.GET("/user-or-admin", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.GET("/admin-only", auth.RequireRole(auth.RoleAdmin), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func doRequest(t *testing.T, router *gin.Engine, path, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAuth_MissingToken(t *testing.T) {
	router := newAuthRouter(testTokens())
	rec := doRequest(t, router, "/protected", "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	router := newAuthRouter(testTokens())
	rec := doRequest(t, router, "/protected", "Bearer wrong-token")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// TestAuth_MalformedHeader covers every header shape that isn't a
// well-formed "Bearer <token>", including the ticket's explicit
// "Basic ..." and bare "Bearer" cases.
func TestAuth_MalformedHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"basic scheme", "Basic dXNlcjpwYXNz"},
		{"bearer with no token", "Bearer"},
		{"bearer with trailing space only", "Bearer "},
		{"lowercase scheme", "bearer " + testToken},
		{"no scheme at all", testToken},
		{"empty header value", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newAuthRouter(testTokens())
			rec := doRequest(t, router, "/protected", tt.header)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}

func TestAuth_ValidToken(t *testing.T) {
	router := newAuthRouter(testTokens())
	rec := doRequest(t, router, "/protected", "Bearer "+testToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("body = %s, want the handler's own response — the request should have reached it", rec.Body.String())
	}
}

// TestAuth_ValidAdminToken proves the admin token also authenticates
// successfully on a route that doesn't require any particular role —
// admin is a valid caller too, just a more privileged one.
func TestAuth_ValidAdminToken(t *testing.T) {
	router := newAuthRouter(testTokens())
	rec := doRequest(t, router, "/protected", "Bearer "+testAdminToken)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestAuth_ClientKey_DistinctPerToken proves 7.7.3's requirement that
// admin and user identities never accidentally share a rate-limit
// bucket: ClientKey is derived from which token authenticated the
// request, not from Role, so two different tokens always produce two
// different ClientKeys even though there are only two roles.
func TestAuth_ClientKey_DistinctPerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(auth.Middleware(testTokens()))
	var got auth.Identity
	router.GET("/protected", func(c *gin.Context) {
		got, _ = auth.IdentityFromContext(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	doRequest(t, router, "/protected", "Bearer "+testToken)
	userKey := got.ClientKey

	doRequest(t, router, "/protected", "Bearer "+testAdminToken)
	adminKey := got.ClientKey

	if userKey == "" || adminKey == "" {
		t.Fatalf("ClientKey should never be empty for an authenticated request: user=%q admin=%q", userKey, adminKey)
	}
	if userKey == adminKey {
		t.Error("user and admin tokens produced the same ClientKey — they would share a rate-limit bucket")
	}
}

// TestAuth_ClientKey_SameTokenSameKey proves ClientKey is stable for
// repeated requests with the same token — a rate limiter needs the
// same caller to always land in the same bucket.
func TestAuth_ClientKey_SameTokenSameKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(auth.Middleware(testTokens()))
	var keys []string
	router.GET("/protected", func(c *gin.Context) {
		identity, _ := auth.IdentityFromContext(c)
		keys = append(keys, identity.ClientKey)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	doRequest(t, router, "/protected", "Bearer "+testToken)
	doRequest(t, router, "/protected", "Bearer "+testToken)

	if len(keys) != 2 || keys[0] != keys[1] {
		t.Errorf("ClientKey across two requests with the same token = %v, want identical values", keys)
	}
}

// TestAuth_DoesNotLeakToken proves a rejection's response never echoes
// back a configured token, the caller's wrong guess, or anything else
// that could confirm/deny part of it. The fixed {"error":
// "unauthorized"} body used for every rejection path already
// guarantees this — this test pins that guarantee against regression.
func TestAuth_DoesNotLeakToken(t *testing.T) {
	router := newAuthRouter(testTokens())

	for _, header := range []string{"Bearer wrong-guess", "Bearer " + testToken[:len(testToken)-1], "Basic " + testToken} {
		rec := doRequest(t, router, "/protected", header)
		body := rec.Body.String()
		if body != `{"error":"unauthorized"}` {
			t.Errorf("header %q: body = %s, want the fixed unauthorized body with nothing token-related in it", header, body)
		}
	}
}

func TestAuth_DifferentLengthToken_Rejected(t *testing.T) {
	router := newAuthRouter(testTokens())
	rec := doRequest(t, router, "/protected", "Bearer "+testToken+"-extra")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- 7.7.2: authorization (RequireRole) ---

// TestAuthz_User_NormalEndpoint_OK proves an authenticated user (not
// admin) reaches a route that carries no role requirement.
func TestAuthz_User_NormalEndpoint_OK(t *testing.T) {
	router := newRoleRouter(testTokens())
	rec := doRequest(t, router, "/user-or-admin", "Bearer "+testToken)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestAuthz_User_AdminEndpoint_Forbidden is the ticket's central
// distinction: a user token is genuinely authenticated (it's valid),
// but not authorized for an admin-only route — 403, not 401.
func TestAuthz_User_AdminEndpoint_Forbidden(t *testing.T) {
	router := newRoleRouter(testTokens())
	rec := doRequest(t, router, "/admin-only", "Bearer "+testToken)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// TestAuthz_Admin_AdminEndpoint_Allowed proves the admin token reaches
// an admin-only route.
func TestAuthz_Admin_AdminEndpoint_Allowed(t *testing.T) {
	router := newRoleRouter(testTokens())
	rec := doRequest(t, router, "/admin-only", "Bearer "+testAdminToken)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestAuthz_Admin_NormalEndpoint_Allowed proves admin is a strict
// superset of user (the table in the 7.7.2 ticket: every
// user-accessible route is also admin-accessible) — not a disjoint
// role that would need its own separate grant.
func TestAuthz_Admin_NormalEndpoint_Allowed(t *testing.T) {
	router := newRoleRouter(testTokens())
	rec := doRequest(t, router, "/user-or-admin", "Bearer "+testAdminToken)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestAuthz_MissingToken_Unauthorized proves the 401/403 boundary from
// the other side: no token at all on an admin-only route is a 401
// (authentication failure — Middleware never lets the request reach
// RequireRole), never a 403.
func TestAuthz_MissingToken_Unauthorized(t *testing.T) {
	router := newRoleRouter(testTokens())
	rec := doRequest(t, router, "/admin-only", "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d — no token is an authentication failure, not an authorization one", rec.Code, http.StatusUnauthorized)
	}
}

// TestAuthz_InvalidToken_Unauthorized is the same boundary for a wrong
// (rather than missing) token: still 401, not 403 — the caller was
// never authenticated in the first place, so there's no identity for
// RequireRole to have rejected.
func TestAuthz_InvalidToken_Unauthorized(t *testing.T) {
	router := newRoleRouter(testTokens())
	rec := doRequest(t, router, "/admin-only", "Bearer wrong-token")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d — a wrong token is an authentication failure, not an authorization one", rec.Code, http.StatusUnauthorized)
	}
}

// TestAuthz_RequireRole_NoIdentity_Forbidden proves RequireRole fails
// closed (403) if it somehow runs without Middleware having set an
// Identity first — a misconfigured route, not a normal request, but
// one that must never be silently treated as authorized.
func TestAuthz_RequireRole_NoIdentity_Forbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/admin-only", auth.RequireRole(auth.RoleAdmin), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	rec := doRequest(t, router, "/admin-only", "")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestAuthz_RequireRole_WithIdentity_ExactRoute exercises RequireRole
// directly against a hand-set Identity (SetIdentity), independent of
// Middleware — proving the authorization decision itself is correct
// for both roles without needing a token in the picture at all.
func TestAuthz_RequireRole_WithIdentity(t *testing.T) {
	tests := []struct {
		name string
		role auth.Role
		want int
	}{
		{"user identity on admin route", auth.RoleUser, http.StatusForbidden},
		{"admin identity on admin route", auth.RoleAdmin, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.GET("/admin-only", func(c *gin.Context) {
				auth.SetIdentity(c, auth.Identity{Role: tt.role})
				c.Next()
			}, auth.RequireRole(auth.RoleAdmin), func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			rec := doRequest(t, router, "/admin-only", "")

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
