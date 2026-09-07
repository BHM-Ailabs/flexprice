package v1

import (
	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/gin-gonic/gin"
	"net/http"
)

type AIAssistantHandler struct {
	ai  *service.OpenRouterService
	cfg *config.Configuration
}

func NewAIAssistantHandler(ai *service.OpenRouterService, cfg *config.Configuration) *AIAssistantHandler {
	return &AIAssistantHandler{ai: ai, cfg: cfg}
}

// Chat answers questions using permission-scoped billing records.
// @Summary Ask the dashboard assistant
// @ID askDashboardAssistant
// @Tags AI
// @Accept json
// @Produce json
// @Param request body service.AssistantRequest true "Conversation"
// @Success 200 {object} service.AssistantResponse
// @Failure 400 {object} map[string]string
// @Security ApiKeyAuth
// @x-scope "read"
// @Router /ai/assistant [post]
func (h *AIAssistantHandler) Chat(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 96*1024)
	var req service.AssistantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.NewError("Invalid assistant request").WithHint("Invalid assistant request").Mark(ierr.ErrValidation))
		return
	}
	credential := http.Header{}
	if token := c.GetHeader(types.HeaderAuthorization); token != "" {
		credential.Set(types.HeaderAuthorization, token)
	}
	if key := c.GetHeader(h.cfg.Auth.APIKey.Header); key != "" {
		credential.Set(h.cfg.Auth.APIKey.Header, key)
	}
	result, err := h.ai.Chat(c.Request.Context(), req, credential)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// ParsePricing generates a pricing preview without creating records.
// @Summary Preview pricing with OpenRouter
// @ID parsePricing
// @Tags AI
// @Accept json
// @Produce json
// @Param request body dto.ParseGeminiPricingRequest true "Pricing description and output schema"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Security ApiKeyAuth
// @x-scope "read"
// @Router /ai/pricing/parse [post]
func (h *AIAssistantHandler) ParsePricing(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 96*1024)
	var req dto.ParseGeminiPricingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.NewError("Invalid pricing request").WithHint("Invalid pricing request").Mark(ierr.ErrValidation))
		return
	}
	if err := req.Validate(); err != nil {
		c.Error(err)
		return
	}
	result, err := h.ai.ParsePricing(c.Request.Context(), &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.Data(http.StatusOK, "application/json", result)
}
