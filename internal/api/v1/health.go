package v1

import (
	"github.com/flexprice/flexprice/internal/ee/service"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/gin-gonic/gin"
	"net/http"
)

type HealthHandler struct{ readiness service.HealthService }

func NewHealthHandler(_ *logger.Logger, readiness service.HealthService) *HealthHandler {
	return &HealthHandler{readiness: readiness}
}

// @Summary Liveness check
// @ID healthCheck
// @Description Process liveness only; use /health/ready for dependency readiness.
// @Tags Health
// @Produce json
// @Success 200 {object} map[string]string
// @Router /health [get]
func (h *HealthHandler) Health(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }

// @Summary Dependency readiness check
// @ID readinessCheck
// @Description Checks PostgreSQL, ClickHouse, Kafka and configured Temporal within two seconds. Does not prove consumer progress or billing workflow correctness.
// @Tags Health
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /health/ready [get]
func (h *HealthHandler) Ready(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.readiness == nil || !h.readiness.Ready(c.Request.Context()) {
		c.Header("Retry-After", "5")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "code": "service_unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
