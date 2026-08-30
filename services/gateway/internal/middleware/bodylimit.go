package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxBodyBytes returns Gin middleware that rejects a request body
// larger than limit bytes (7.7.6) — a generic safety net against
// memory exhaustion from an oversized body, enforced before any
// handler (or its JSON/multipart binding) ever reads it.
//
// http.MaxBytesReader makes the underlying Read fail once limit is
// exceeded, rather than silently truncating, so a handler's own
// c.ShouldBindJSON or multipart parser naturally surfaces that as a
// decode error instead of quietly processing a truncated payload.
//
// Applying this more than once on the same request (e.g. a generous
// limit for the whole API, chained with a tighter one on a specific
// JSON-only route) composes correctly: whichever limit is smaller is
// the one that actually trips, since each wraps the body in its own
// bounded reader.
func MaxBodyBytes(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
