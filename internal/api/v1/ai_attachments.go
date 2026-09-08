package v1

import (
	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/gin-gonic/gin"
	"net/http"
)

// UploadAttachment prepares one file for an AI conversation or pricing preview.
// @Summary Attach a file to dashboard AI
// @ID uploadAIAttachment
// @Tags AI
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "PDF, Office, image, text or short video; 20 MB maximum"
// @Success 200 {object} service.AIAttachmentInfo
// @Security ApiKeyAuth
// @Router /ai/attachments [post]
func (h *AIAssistantHandler) UploadAttachment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxAIFileBytes+1024*1024)
	if err := c.Request.ParseMultipartForm(1024 * 1024); err != nil {
		c.Error(ierr.NewError("Invalid or oversized file upload").WithHint("Choose one file up to 20 MB.").Mark(ierr.ErrValidation))
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	form := c.Request.MultipartForm
	if form == nil || len(form.File) != 1 || len(form.File["file"]) != 1 || len(form.Value) != 0 {
		c.Error(ierr.NewError("One file required").WithHint("Upload one file per request.").Mark(ierr.ErrValidation))
		return
	}
	file := form.File["file"][0]
	reader, err := file.Open()
	if err != nil {
		c.Error(err)
		return
	}
	defer reader.Close()
	ctx := service.WithAIAttachmentPrincipal(c.Request.Context(), c.GetHeader(h.cfg.Auth.APIKey.Header))
	result, err := h.ai.UploadAttachment(ctx, file.Filename, reader)
	if err != nil {
		c.Error(err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

// AttachmentStatus returns file readiness within the caller's identity and environment.
// @Summary Get AI attachment status
// @ID getAIAttachment
// @Tags AI
// @Produce json
// @Param id path string true "Attachment ID"
// @Success 200 {object} service.AIAttachmentInfo
// @Security ApiKeyAuth
// @Router /ai/attachments/{id} [get]
func (h *AIAssistantHandler) AttachmentStatus(c *gin.Context) {
	ctx := service.WithAIAttachmentPrincipal(c.Request.Context(), c.GetHeader(h.cfg.Auth.APIKey.Header))
	result, err := h.ai.AttachmentStatus(ctx, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

// DeleteAttachment removes a caller-owned temporary AI attachment.
// @Summary Remove an AI attachment
// @ID deleteAIAttachment
// @Tags AI
// @Param id path string true "Attachment ID"
// @Success 204
// @Security ApiKeyAuth
// @Router /ai/attachments/{id} [delete]
func (h *AIAssistantHandler) DeleteAttachment(c *gin.Context) {
	ctx := service.WithAIAttachmentPrincipal(c.Request.Context(), c.GetHeader(h.cfg.Auth.APIKey.Header))
	if err := h.ai.DeleteAttachment(ctx, c.Param("id")); err != nil {
		c.Error(err)
		return
	}
	c.Status(http.StatusNoContent)
}
