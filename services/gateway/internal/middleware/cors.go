package middleware

import "github.com/gin-gonic/gin"

// CORS allows browser JS on any origin to call the gateway's HTTP API —
// the frontend calls /api/v1/analyze-frame directly via fetch(), from a
// different origin than the gateway. This matches the signaling
// WebSocket's already-permissive CheckOrigin; both should be revisited
// once the frontend's real origin is known.
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}
