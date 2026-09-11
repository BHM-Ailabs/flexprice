package service

import (
	"context"
	domain "github.com/flexprice/flexprice/internal/domain/invoice"
	ierr "github.com/flexprice/flexprice/internal/errors"
)

func (s *invoiceService) referenceRepository() (domain.ReferenceRepository, error) {
	repo, ok := s.InvoiceRepo.(domain.ReferenceRepository)
	if !ok {
		return nil, ierr.NewError("invoice reference registry unavailable").Mark(ierr.ErrPermissionDenied)
	}
	return repo, nil
}
func (s *invoiceService) ReservePublicReference(ctx context.Context, req domain.ReferenceRequest) (*domain.PublicReference, error) {
	repo, err := s.referenceRepository()
	if err != nil {
		return nil, err
	}
	return repo.ReservePublicReference(ctx, req)
}
func (s *invoiceService) BindPublicReference(ctx context.Context, id string, req domain.ReferenceRequest) (*domain.PublicReference, error) {
	repo, err := s.referenceRepository()
	if err != nil {
		return nil, err
	}
	return repo.BindPublicReference(ctx, id, req)
}
func (s *invoiceService) BackfillPublicReferences(ctx context.Context, after string, limit int, apply bool) (map[string]interface{}, error) {
	repo, err := s.referenceRepository()
	if err != nil {
		return nil, err
	}
	return repo.BackfillPublicReferences(ctx, after, limit, apply)
}
