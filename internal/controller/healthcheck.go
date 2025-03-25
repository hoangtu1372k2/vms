package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Health		 godoc
// @Summary      Health is used to handle HTTP Health requests to this service.
// @Description  Use this for liveness probes or any other checks which only validate if the services is running.
// @Tags         healthcheck
// @Success      200
// @Router       /health [get]
func Health(c *gin.Context) {
	c.String(http.StatusOK, "Success")
}
