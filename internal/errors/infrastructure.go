package errors

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net"
	"reflect"
	"syscall"

	"github.com/cockroachdb/errors"
	"github.com/lib/pq"
)

// IsInfrastructureFailure recognizes typed availability failures, never arbitrary
// driver messages (which can describe deterministic data/constraint errors).
func IsInfrastructureFailure(err error) bool {
	return infrastructureReason(err) != ""
}

func infrastructureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "host_unreachable"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network_unreachable"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "network_timeout"
	case errors.Is(err, syscall.EPIPE):
		return "broken_pipe"
	case errors.Is(err, driver.ErrBadConn), errors.Is(err, sql.ErrConnDone):
		return "connection_unavailable"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection_closed"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "network_timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_unavailable"
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return "network_operation_failed"
	}
	var pg *pq.Error
	if errors.As(err, &pg) {
		if pg.Code == "53300" {
			return "database_pool_exhausted"
		}
		switch pg.Code.Class() {
		case "08":
			return "database_connection_unavailable"
		case "53":
			return "database_resources_exhausted"
		case "58":
			return "database_system_failure"
		}
		switch pg.Code {
		case "57014":
			// lib/pq can report query_canceled instead of DeadlineExceeded
			// when a request times out during a query. Explicit cancellation
			// also leaves the request incomplete; neither is a domain failure.
			return "database_query_canceled"
		case "57P01", "57P02", "57P03":
			return "database_unavailable"
		}
		// XX000 alone does not prove temporary unavailability: server internal
		// errors can also be persistent bugs. Keep it at 500 unless another
		// typed infrastructure cause exists in the chain.
	}
	return ""
}

func hasDeterministicDatabaseCause(err error) bool {
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	var pg *pq.Error
	if errors.As(err, &pg) && (pg.Code.Class() == "22" || pg.Code.Class() == "23") {
		return true
	}
	// Importing the generated Ent package here would form a cycle:
	// ent -> internal/types -> internal/errors. Match concrete type identity
	// (package path + name), never error text, as Ent's Is* helpers do with As.
	for err != nil {
		t := reflect.TypeOf(err)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.PkgPath() == "github.com/flexprice/flexprice/ent" {
			switch t.Name() {
			case "NotFoundError", "ValidationError", "ConstraintError":
				return true
			}
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				if hasDeterministicDatabaseCause(cause) {
					return true
				}
			}
			return false
		}
		err = errors.UnwrapOnce(err)
	}
	return false
}

// SanitizedDependencyError retains the underlying error's type and known safe
// reason without logging its message, DSN, query, key, or driver Detail fields.
func SanitizedDependencyError(err error) string {
	reason := infrastructureReason(err)
	if reason == "" {
		reason = "unclassified"
	}
	var pg *pq.Error
	if errors.As(err, &pg) && validSQLState(string(pg.Code)) {
		return fmt.Sprintf("%T: %s (sqlstate=%s)", errors.UnwrapAll(err), reason, pg.Code)
	}
	return fmt.Sprintf("%T: %s", errors.UnwrapAll(err), reason)
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
