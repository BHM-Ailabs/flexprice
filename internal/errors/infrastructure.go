package errors

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net"
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
	var pg *pq.Error
	if errors.As(err, &pg) {
		switch pg.Code.Class() {
		case "08":
			return "database_connection_unavailable"
		}
		switch pg.Code {
		case "53300":
			return "database_pool_exhausted"
		case "57P01", "57P02", "57P03":
			return "database_unavailable"
		}
	}
	return ""
}

// SanitizedDependencyError retains the underlying error's type and known safe
// reason without logging its message, DSN, query, key, or driver Detail fields.
func SanitizedDependencyError(err error) string {
	reason := infrastructureReason(err)
	if reason == "" {
		reason = "unclassified"
	}
	return fmt.Sprintf("%T: %s", errors.UnwrapAll(err), reason)
}
