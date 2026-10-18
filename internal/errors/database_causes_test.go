package errors_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flexprice/flexprice/ent"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestDatabaseDomainCauses(t *testing.T) {
	// Use the real generated Ent types to protect the cycle-free type checks
	// in internal/errors (Ent itself imports errors via internal/types).
	// No SQL driver is attached: required-field validation happens before I/O.
	client := ent.NewClient()
	_, validation := client.Customer.Create().Save(context.Background())
	require.True(t, ent.IsValidationError(validation))
	for _, tt := range []struct {
		name   string
		cause  error
		marker error
		status int
	}{
		{"ent not found", &ent.NotFoundError{}, ierr.ErrNotFound, 404},
		{"sql not found", sql.ErrNoRows, ierr.ErrNotFound, 404},
		{"ent validation", validation, ierr.ErrValidation, 400},
		{"ent constraint", &ent.ConstraintError{}, ierr.ErrValidation, 400},
		{"pq data", &pq.Error{Code: "22003"}, ierr.ErrValidation, 400},
		{"pq constraint", &pq.Error{Code: "23505"}, ierr.ErrValidation, 400},
		{"joined ent cause", errors.Join(errors.New("context"), &ent.NotFoundError{}), ierr.ErrNotFound, 404},
		{"text is not evidence", errors.New("ent: customer not found"), ierr.ErrNotFound, 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, databaseOuter := range []bool{false, true} {
				var err error
				if databaseOuter {
					err = ierr.WithError(ierr.WithError(tt.cause).Mark(tt.marker)).Mark(ierr.ErrDatabase)
				} else {
					err = ierr.WithError(ierr.WithError(tt.cause).Mark(ierr.ErrDatabase)).Mark(tt.marker)
				}
				status, _ := ierr.ResolveError(err)
				require.Equal(t, tt.status, status)
			}
		})
	}
}
