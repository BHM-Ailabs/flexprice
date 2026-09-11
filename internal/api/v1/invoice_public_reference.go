package v1

import (
	"net/http"

	domain "github.com/flexprice/flexprice/internal/domain/invoice"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/gin-gonic/gin"
)

func (h *InvoiceHandler) referenceService(c *gin.Context) domain.ReferenceRepository {
	svc, ok := h.invoiceService.(domain.ReferenceRepository)
	if !ok {
		c.Error(ierr.NewError("invoice reference service unavailable").Mark(ierr.ErrPermissionDenied))
		return nil
	}
	return svc
}

// ReservePublicReference reserves an idempotent short number before customer invoice creation.
// @Summary Reserve invoice public reference
// @ID reserveInvoicePublicReference
// @Tags Invoices
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param request body invoice.ReferenceRequest true "Stable reservation evidence"
// @Success 200 {object} invoice.PublicReference
// @Router /invoices/references/reserve [post]
func (h *InvoiceHandler) ReservePublicReference(c *gin.Context) {
	var req domain.ReferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	svc := h.referenceService(c)
	if svc == nil {
		return
	}
	ref, err := svc.ReservePublicReference(c.Request.Context(), req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, ref)
}

// BindPublicReference binds a reservation or appends aliases to an attributed invoice.
// @Summary Bind invoice public reference
// @ID bindInvoicePublicReference
// @Tags Invoices
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Invoice ID"
// @Param request body invoice.ReferenceRequest true "Verified customer and aliases"
// @Success 200 {object} invoice.PublicReference
// @Router /invoices/{id}/reference [post]
func (h *InvoiceHandler) BindPublicReference(c *gin.Context) {
	var req domain.ReferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	svc := h.referenceService(c)
	if svc == nil {
		return
	}
	ref, err := svc.BindPublicReference(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, ref)
}

type invoiceReferenceBackfillRequest struct {
	AfterID string `json:"after_id"`
	Limit   int    `json:"limit"`
	Apply   bool   `json:"apply"`
}

// BackfillPublicReferences previews or applies bounded reference assignments.
// @Summary Backfill invoice public references
// @ID backfillInvoicePublicReferences
// @Tags Invoices
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param request body invoiceReferenceBackfillRequest true "Bounded cursor, preview by default"
// @Success 200 {object} map[string]interface{}
// @Router /invoices/references/backfill [post]
func (h *InvoiceHandler) BackfillPublicReferences(c *gin.Context) {
	var req invoiceReferenceBackfillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	if req.Limit == 0 {
		req.Limit = 100
	}
	svc := h.referenceService(c)
	if svc == nil {
		return
	}
	result, err := svc.BackfillPublicReferences(c.Request.Context(), req.AfterID, req.Limit, req.Apply)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}
