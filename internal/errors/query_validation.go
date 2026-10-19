package errors

// queryValidationError records rejection by a pure query-field resolver before
// SQL is executed. A contextual validation marker on a database error is not
// equivalent evidence.
type queryValidationError struct {
	cause error
}

func (e *queryValidationError) Error() string {
	if e == nil || e.cause == nil {
		return "invalid query"
	}
	return e.cause.Error()
}

func (e *queryValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// WithQueryValidation is only for pure, local query-field resolution. Never use
// it to reclassify a repository/driver failure or an error returned after I/O.
func WithQueryValidation(err error) error {
	if err == nil || !IsValidation(err) || IsDatabase(err) || IsInfrastructureFailure(err) || IsServiceUnavailable(err) {
		return err
	}
	return &queryValidationError{cause: err}
}
