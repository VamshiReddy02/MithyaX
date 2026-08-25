package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthResponse is the JSON body returned by the health endpoint.
type HealthResponse struct {
	Status string `json:"status"`
}

// Health reports that the service is up.
func Health(c *gin.Context) {
	c.JSON(http.StatusOK, HealthResponse{Status: "ok"})
}
