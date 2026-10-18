package errors

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestDatabaseHTTPClassification(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
		code   ErrorCode
	}{
		{"not found", WithError(WithError(sql.ErrNoRows).Mark(ErrNotFound)).Mark(ErrDatabase), 404, ErrCodeNotFound},
		{"validation", WithError(WithError(&pq.Error{Code: "22003"}).Mark(ErrValidation)).Mark(ErrDatabase), 400, ErrCodeValidation},
		{"permission", ErrPermissionDenied, 403, ErrCodePermissionDenied},
		{"domain not found", ErrNotFound, 404, ErrCodeNotFound},
		{"domain validation", ErrValidation, 400, ErrCodeValidation},
		{"unproven not found", WithError(ErrNotFound).Mark(ErrDatabase), 500, ErrCodeDatabase},
		{"unproven validation", WithError(ErrValidation).Mark(ErrDatabase), 500, ErrCodeDatabase},
		{"unproven permission", WithError(ErrPermissionDenied).Mark(ErrDatabase), 500, ErrCodeDatabase},
		{"constraint", WithError(&pq.Error{Code: "23505"}).Mark(ErrDatabase), 500, ErrCodeDatabase},
		{"invalid data", WithError(&pq.Error{Code: "22003"}).Mark(ErrDatabase), 500, ErrCodeDatabase},
		{"unknown", ErrDatabase, 500, ErrCodeDatabase},
		{"unknown with not found", WithError(WithError(errors.New("unknown database outage")).Mark(ErrDatabase)).Mark(ErrNotFound), 500, ErrCodeDatabase},
		{"unknown with validation", WithError(WithError(errors.New("unknown database outage")).Mark(ErrDatabase)).Mark(ErrValidation), 500, ErrCodeDatabase},
		{"internal with not found", WithError(WithError(&pq.Error{Code: "XX000"}).Mark(ErrDatabase)).Mark(ErrNotFound), 500, ErrCodeDatabase},
		{"refused", WithError(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}).Mark(ErrDatabase), 503, ErrCodeDatabase},
		{"reset", WithError(syscall.ECONNRESET).Mark(ErrDatabase), 503, ErrCodeDatabase},
		{"timeout", WithError(context.DeadlineExceeded).Mark(ErrDatabase), 503, ErrCodeServiceUnavailable},
		{"bad connection", WithError(driver.ErrBadConn).Mark(ErrDatabase), 503, ErrCodeDatabase},
		{"pool", WithError(&pq.Error{Code: "53300"}).Mark(ErrDatabase), 503, ErrCodeDatabase},
		{"recovery", WithError(&pq.Error{Code: "57P03"}).Mark(ErrDatabase), 503, ErrCodeDatabase},
		{"connection", WithError(&pq.Error{Code: "08006"}).Mark(ErrDatabase), 503, ErrCodeDatabase},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, code := ResolveError(tt.err)
			require.Equal(t, tt.status, status)
			require.Equal(t, tt.code, code)
		})
	}
}

func TestSanitizedDependencyError(t *testing.T) {
	require.Equal(t, "syscall.Errno: connection_refused", SanitizedDependencyError(WithError(syscall.ECONNREFUSED).WithMessage("synthetic secret DSN").Mark(ErrDatabase)))
	require.Equal(t, "*pq.Error: database_pool_exhausted (sqlstate=53300)", SanitizedDependencyError(&pq.Error{Code: "53300", Message: "synthetic password", Detail: "synthetic key"}))
	require.Equal(t, "*pq.Error: database_resources_exhausted (sqlstate=53100)", SanitizedDependencyError(&pq.Error{Code: "53100", Message: "synthetic password", Detail: "synthetic key"}))
	require.Equal(t, "*pq.Error: unclassified (sqlstate=XX000)", SanitizedDependencyError(&pq.Error{Code: "XX000", Message: "synthetic password", Detail: "synthetic key"}))
	require.Equal(t, "*net.DNSError: dns_unavailable", SanitizedDependencyError(&net.DNSError{Err: "synthetic password", Name: "synthetic host", IsNotFound: true}))
	require.Equal(t, "syscall.Errno: host_unreachable", SanitizedDependencyError(syscall.EHOSTUNREACH))
	for _, code := range []pq.ErrorCode{"password", "58\n00", "58;00", "58p01"} {
		require.NotContains(t, SanitizedDependencyError(&pq.Error{Code: code}), "sqlstate=")
	}
	require.Equal(t, "*errors.errorString: unclassified", SanitizedDependencyError(errors.New("synthetic credential")))
	require.Equal(t, http.StatusServiceUnavailable, func() int { s, _ := ResolveError(WithError(syscall.ECONNREFUSED).Mark(ErrDatabase)); return s }())
}
