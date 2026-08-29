package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// forbiddenBody is the fixed response for every authorization
// rejection — distinct from unauthorizedBody (middleware.go) because
// the two mean different things to a caller: 401 means "prove who you
// are," 403 means "you did, but you still can't do this."
var forbiddenBody = gin.H{"error": "forbidden"}

// roleRank orders the two roles so RequireRole can treat admin as a
// strict superset of user (see the table in Phase 7.7.2: every
// user-accessible route is admin-accessible too, plus admin-only
// ones) without a general permissions system — just "is this role at
// least as privileged as the one required."
var roleRank = map[Role]int{
	RoleUser:  1,
	RoleAdmin: 2,
}

// RequireRole returns Gin middleware that must run after Middleware
// (see httpserver.New's route wiring) — it authorizes based on the
// Identity Middleware already attached to the context, never touching
// the request's token itself. A caller whose role doesn't rank at
// least as high as minimum gets 403 Forbidden, not 401: by the time
// this runs, Middleware has already established who they are, so the
// remaining question is purely "are they allowed," not "are they who
// they claim."
//
// Missing Identity (Middleware never ran, or ran on a route this one
// was mistakenly not chained after) fails closed as 403 rather than
// assuming a role — there's no valid identity to authorize, so it can
// never be treated as sufficiently privileged.
func RequireRole(minimum Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromContext(c)
		if !ok || roleRank[identity.Role] < roleRank[minimum] {
			c.AbortWithStatusJSON(http.StatusForbidden, forbiddenBody)
			return
		}
		c.Next()
	}
}
