package errors_test

import (
	"errors"
	"fmt"
	"net"
	"testing"

	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestQueryValidationProof(t *testing.T) {
	require.NoError(t, ierr.WithQueryValidation(nil))
	validation := ierr.NewError("unknown local query field").Mark(ierr.ErrValidation)
	proof := ierr.WithQueryValidation(validation)
	require.ErrorIs(t, proof, validation)
	for _, wrapped := range []error{
		proof,
		ierr.WithError(proof).Mark(ierr.ErrDatabase),
		ierr.WithError(fmt.Errorf("query construction: %w", proof)).Mark(ierr.ErrDatabase),
	} {
		status, code := ierr.ResolveError(wrapped)
		require.Equal(t, 400, status)
		require.Equal(t, ierr.ErrCodeValidation, code)
	}
	// A real outage always wins even if a distinct local query error was joined.
	outage := ierr.WithError(errors.Join(proof, &pq.Error{Code: "53100"})).Mark(ierr.ErrDatabase)
	status, _ := ierr.ResolveError(outage)
	require.Equal(t, 503, status)
}

func TestQueryValidationRejectsUnprovenCauses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{"not validation", errors.New("unknown failure"), 500},
		{"unknown DB under validation", ierr.WithError(ierr.WithError(errors.New("unknown failure")).Mark(ierr.ErrDatabase)).Mark(ierr.ErrValidation), 500},
		{"DNS under validation", ierr.WithError(&net.DNSError{IsNotFound: true}).Mark(ierr.ErrValidation), 503},
		{"resource outage under validation", ierr.WithError(&pq.Error{Code: "53100"}).Mark(ierr.ErrValidation), 503},
		{"service unavailable under validation", ierr.WithError(ierr.ErrServiceUnavailable).Mark(ierr.ErrValidation), 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proof := ierr.WithQueryValidation(tt.err)
			require.Same(t, tt.err, proof, "rejected inputs must not acquire local validation evidence")
			status, _ := ierr.ResolveError(ierr.WithError(proof).Mark(ierr.ErrDatabase))
			require.Equal(t, tt.status, status)
		})
	}
}
