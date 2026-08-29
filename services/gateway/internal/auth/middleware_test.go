package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/auth"
)

const testToken = "test-development-token"

func newAuthRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(auth.Middleware(testToken))
	router.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func doRequest(t *testing.T, router *gin.Engine, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAuth_MissingToken(t *testing.T) {
	router := newAuthRouter()
	rec := doRequest(t, router, "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	router := newAuthRouter()
	rec := doRequest(t, router, "Bearer wrong-token")

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
			router := newAuthRouter()
			rec := doRequest(t, router, tt.header)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}

func TestAuth_ValidToken(t *testing.T) {
	router := newAuthRouter()
	rec := doRequest(t, router, "Bearer "+testToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("body = %s, want the handler's own response — the request should have reached it", rec.Body.String())
	}
}

// TestAuth_DoesNotLeakToken proves a rejection's response never echoes
// back the configured token, the caller's wrong guess, or anything
// else that could confirm/deny part of it. The fixed {"error":
// "unauthorized"} body used for every rejection path already
// guarantees this — this test pins that guarantee against regression.
func TestAuth_DoesNotLeakToken(t *testing.T) {
	router := newAuthRouter()

	for _, header := range []string{"Bearer wrong-guess", "Bearer " + testToken[:len(testToken)-1], "Basic " + testToken} {
		rec := doRequest(t, router, header)
		body := rec.Body.String()
		if body != `{"error":"unauthorized"}` {
			t.Errorf("header %q: body = %s, want the fixed unauthorized body with nothing token-related in it", header, body)
		}
	}
}

func TestAuth_DifferentLengthToken_Rejected(t *testing.T) {
	router := newAuthRouter()
	rec := doRequest(t, router, "Bearer "+testToken+"-extra")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
