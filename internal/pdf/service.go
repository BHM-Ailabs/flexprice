package pdf

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/domain/pdf"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/flexprice/flexprice/internal/typst"
	"github.com/samber/lo"
	qrcode "github.com/skip2/go-qrcode"
)

// Generator defines the interface for PDF generation operations
type Generator interface {
	RenderInvoicePdf(ctx context.Context, data *pdf.InvoiceData, templateName *types.TemplateName) ([]byte, error)
}

type Config struct {
}

type service struct {
	config Config
	typst  typst.Compiler
}

// NewGenerator creates a new PDF service
func NewGenerator(config *config.Configuration, typst typst.Compiler) Generator {
	return &service{
		config: Config{},
		typst:  typst,
	}
}

// RenderPdf implements Service.RenderPdf
func (s *service) RenderInvoicePdf(ctx context.Context, data *pdf.InvoiceData, templateName *types.TemplateName) ([]byte, error) {
	if data.PlaqadBranding && data.AccountURL != "" {
		qr, err := qrcode.New(data.AccountURL, qrcode.Medium)
		if err != nil {
			return nil, ierr.WithError(err).WithHint("failed to encode invoice account link").Mark(ierr.ErrSystem)
		}
		copy := *data
		copy.AccountQRSVG = qrSVG(qr.Bitmap())
		data = &copy
	}
	// todo: template management from caller
	template := types.TemplateInvoiceDefault
	if templateName != nil {
		template = lo.FromPtr(templateName)
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, ierr.WithError(err).
			WithHint("failed to marshal invoice data").
			Mark(ierr.ErrSystem)
	}

	pdf, err := s.typst.CompileTemplate(
		template,
		jsonData,
		typst.WithOutputFile(fmt.Sprintf("invoice-%s.pdf", data.ID)),
	)

	if err != nil {
		return nil, ierr.WithError(err).
			WithHint("failed to compile invoice template").
			Mark(ierr.ErrSystem)
	}

	return pdf, nil
}

// QR SVG contains only generated numeric rectangles, with the encoder's quiet
// zone preserved. No external assets or provider tokens are embedded.
func qrSVG(bitmap [][]bool) string {
	var svg strings.Builder
	svg.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d"><rect width="100%%" height="100%%" fill="white"/><g fill="black">`, len(bitmap), len(bitmap)))
	for y, row := range bitmap {
		for x, filled := range row {
			if filled {
				svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="1" height="1"/>`, x, y))
			}
		}
	}
	svg.WriteString(`</g></svg>`)
	return svg.String()
}
