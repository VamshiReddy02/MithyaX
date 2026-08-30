package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/vamshireddy02/mithyax/gateway/internal/middleware"
)

func newBodyLimitRouter(limit int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.MaxBodyBytes(limit))
	router.POST("/echo", func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"bytes": len(body)})
	})
	return router
}

func TestMaxBodyBytes_WithinLimit_Succeeds(t *testing.T) {
	router := newBodyLimitRouter(1024)

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("a", 100)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestMaxBodyBytes_ExceedsLimit_Rejected(t *testing.T) {
	router := newBodyLimitRouter(100)

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("a", 1000)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d, body = %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

// TestMaxBodyBytes_TighterLimitWinsWhenChained proves two MaxBodyBytes
// calls compose correctly: a generous outer limit followed by a
// tighter one is bounded by the tighter of the two, exactly the
// pattern httpserver.New uses (a generous whole-API cap, plus a
// tighter one just for POST /api/v1/analysis).
func TestMaxBodyBytes_TighterLimitWinsWhenChained(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.MaxBodyBytes(1 << 20)) // generous outer limit
	router.POST("/echo", middleware.MaxBodyBytes(50), func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"bytes": len(body)})
	})

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("a", 200)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d — the tighter, route-specific limit must win", rec.Code, http.StatusRequestEntityTooLarge)
	}
}
