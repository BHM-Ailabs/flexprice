package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"syscall"
	"testing"

	"github.com/flexprice/flexprice/ent"
	"github.com/flexprice/flexprice/internal/types"
)

// newRoutingTestClient builds a Client with distinct writer/reader ent clients
// so tests can assert which endpoint a context routes to. The clients carry no
// driver — no queries are executed.
func newRoutingTestClient() (*Client, *ent.Client, *ent.Client) {
	writer := ent.NewClient()
	reader := ent.NewClient()
	return &Client{
		writerClient: writer,
		readerClient: reader,
		hasReader:    true,
	}, writer, reader
}

func TestReader_DefaultsToReplica(t *testing.T) {
	c, _, reader := newRoutingTestClient()
	ctx := types.WithWriterPinning(context.Background())

	if got := c.Reader(ctx); got != reader {
		t.Fatal("expected read to route to reader before any write")
	}
}

func TestReader_ForceWriterFlag(t *testing.T) {
	c, writer, _ := newRoutingTestClient()
	ctx := types.WithForceWriter(context.Background())

	if got := c.Reader(ctx); got != writer {
		t.Fatal("expected force-writer context to route reads to writer")
	}
}

func TestReader_PinnedAfterWrite(t *testing.T) {
	c, writer, reader := newRoutingTestClient()
	ctx := types.WithWriterPinning(context.Background())

	if got := c.Reader(ctx); got != reader {
		t.Fatal("expected reader before any write")
	}

	// Simulate a write: fetching the writer client pins the unit of work
	if got := c.Writer(ctx); got != writer {
		t.Fatal("expected Writer to return writer client")
	}

	if got := c.Reader(ctx); got != writer {
		t.Fatal("expected reads after a write to route to writer (read-your-writes)")
	}
}

func TestReader_PinFromDerivedContextAffectsParent(t *testing.T) {
	c, writer, reader := newRoutingTestClient()
	root := types.WithWriterPinning(context.Background())

	// A write deep in the call stack on a derived context...
	derived := types.SetTenantID(root, "tenant-1")
	_ = c.Writer(derived)

	// ...pins reads issued later on the root (same request)
	if got := c.Reader(root); got != writer {
		t.Fatal("expected pin set on derived context to affect parent context reads")
	}

	// A separate unit of work stays on the replica
	other := types.WithWriterPinning(context.Background())
	if got := c.Reader(other); got != reader {
		t.Fatal("expected unrelated unit of work to keep reading from replica")
	}
}

func TestReader_UnpinnedContextWithoutHolderStaysOnReplica(t *testing.T) {
	c, _, reader := newRoutingTestClient()
	// Context without a pin holder (e.g. a flow not yet covered by an
	// entrypoint): writes don't pin, reads stay on replica — matches the
	// pre-pinning behavior.
	ctx := context.Background()
	_ = c.Writer(ctx)

	if got := c.Reader(ctx); got != reader {
		t.Fatal("expected context without pin holder to keep reading from replica")
	}
}

// Exercise Ent's real transaction-begin path with an in-process SQL driver;
// no database, credentials, or financial writes are involved.
type failingBeginDriver struct{ err error }
type failingBeginConn struct{ err error }
type failingBeginConnector struct{ err error }

func (c failingBeginConnector) Connect(context.Context) (driver.Conn, error) {
	return failingBeginConn{c.err}, nil
}
func (c failingBeginConnector) Driver() driver.Driver { return failingBeginDriver{c.err} }

func (d failingBeginDriver) Open(string) (driver.Conn, error)  { return failingBeginConn{d.err}, nil }
func (c failingBeginConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c failingBeginConn) Close() error                        { return nil }
func (c failingBeginConn) Begin() (driver.Tx, error)           { return nil, c.err }
func (c failingBeginConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return nil, c.err
}

func TestTransactionBeginAvailability(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{"refused", syscall.ECONNREFUSED, 503},
		{"reset", syscall.ECONNRESET, 503},
		{"host unreachable", syscall.EHOSTUNREACH, 503},
		{"network unreachable", syscall.ENETUNREACH, 503},
		{"disk full", &pq.Error{Code: "53100"}, 503},
		{"out of memory", &pq.Error{Code: "53200"}, 503},
		{"io error", &pq.Error{Code: "58030"}, 503},
		{"query canceled", &pq.Error{Code: "57014"}, 503},
		{"timeout", context.DeadlineExceeded, 503},
		{"pool exhausted", &pq.Error{Code: "53300"}, 503},
		{"recovering", &pq.Error{Code: "57P03"}, 503},
		{"deterministic", &pq.Error{Code: "25001"}, 500},
		{"unclassified internal", &pq.Error{Code: "XX000"}, 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := sql.OpenDB(failingBeginConnector{tt.err})
			defer db.Close()
			client := &Client{writerClient: ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
			err := client.withTx(context.Background(), func(context.Context) error { t.Fatal("callback ran after failed begin"); return nil })
			require.ErrorIs(t, err, tt.err)
			status, _ := ierr.ResolveError(err)
			require.Equal(t, tt.status, status)
		})
	}
}
