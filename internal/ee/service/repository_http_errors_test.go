package service_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/flexprice/flexprice/ent"
	"github.com/flexprice/flexprice/ent/scheduledtask"
	v1 "github.com/flexprice/flexprice/internal/api/v1"
	"github.com/flexprice/flexprice/internal/cache"
	st "github.com/flexprice/flexprice/internal/domain/scheduledtask"
	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/postgres"
	repository "github.com/flexprice/flexprice/internal/repository/ent"
	"github.com/flexprice/flexprice/internal/rest/middleware"
	temporalclient "github.com/flexprice/flexprice/internal/temporal/client"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type domainEmptyConnector struct {
	queryErr error
	queries  *atomic.Int32
}
type domainEmptyDriver struct{}
type domainEmptyConn struct {
	queryErr error
	queries  *atomic.Int32
}
type domainEmptyRows struct {
	count bool
	done  bool
}

func (c domainEmptyConnector) Connect(context.Context) (driver.Conn, error) {
	return domainEmptyConn{queryErr: c.queryErr, queries: c.queries}, nil
}
func (domainEmptyConnector) Driver() driver.Driver          { return domainEmptyDriver{} }
func (domainEmptyDriver) Open(string) (driver.Conn, error)  { return domainEmptyConn{}, nil }
func (domainEmptyConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (domainEmptyConn) Close() error                        { return nil }
func (domainEmptyConn) Begin() (driver.Tx, error)           { return nil, errors.New("must not begin") }
func (c domainEmptyConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.queries != nil {
		c.queries.Add(1)
	}
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return &domainEmptyRows{count: strings.Contains(query, "COUNT(")}, nil
}
func (c domainEmptyConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return driver.RowsAffected(0), nil
}
func (r *domainEmptyRows) Columns() []string {
	if r.count {
		return []string{"count"}
	}
	return scheduledtask.Columns
}
func (*domainEmptyRows) Close() error { return nil }
func (r *domainEmptyRows) Next(values []driver.Value) error {
	if r.count && !r.done {
		r.done = true
		values[0] = int64(0)
		return nil
	}
	return io.EOF
}

type domainReadClient struct {
	postgres.IClient
	client *ent.Client
}

func (c domainReadClient) Reader(context.Context) *ent.Client { return c.client }
func (c domainReadClient) Writer(context.Context) *ent.Client { return c.client }

type domainUnusedTemporal struct{ temporalclient.TemporalClient }

type domainMissCache struct{ cache.RedisCache }

func (domainMissCache) Get(context.Context, string) (interface{}, bool) { return nil, false }

func TestDomainHTTPMissingScheduledTask(t *testing.T) {
	db := sql.OpenDB(domainEmptyConnector{})
	t.Cleanup(func() { _ = db.Close() })
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
	repo := repository.NewScheduledTaskRepository(domainReadClient{client: client}, log)
	_, err := repo.Get(context.Background(), "missing")
	require.True(t, ierr.IsNotFound(err), "the real repository has proven absence")
	var entMissing *ent.NotFoundError
	require.ErrorAs(t, err, &entMissing, "the actual absence cause must survive translation")
	status, _ := ierr.ResolveError(err)
	require.Equal(t, 404, status)
	svc := service.NewScheduledTaskService(repo, nil, domainUnusedTemporal{}, log, nil)
	handler := v1.NewScheduledTaskHandler(svc, log)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.GET("/tasks/scheduled/:id", handler.GetScheduledTask)
	r.POST("/tasks/scheduled/:id/run", handler.TriggerForceRun)
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/tasks/scheduled/missing"},
		{http.MethodPost, "/tasks/scheduled/missing/run"},
	} {
		t.Run(tt.method, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			t.Logf("status=%d retry=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
			require.Equal(t, 404, w.Code)
			require.Empty(t, w.Header().Get("Retry-After"))
		})
	}
}

func TestDomainHTTPScheduledTaskZeroRows(t *testing.T) {
	db := sql.OpenDB(domainEmptyConnector{})
	t.Cleanup(func() { _ = db.Close() })
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
	repo := repository.NewScheduledTaskRepository(domainReadClient{client: client}, log)
	for _, operation := range []func() error{
		func() error {
			return repo.Update(context.Background(), &st.ScheduledTask{
				ID: "missing", ConnectionID: "connection", EntityType: types.ScheduledTaskEntityTypeInvoice, Interval: types.ScheduledTaskIntervalDaily,
				Status: types.StatusPublished, UpdatedBy: "user",
			})
		},
		func() error { return repo.Delete(context.Background(), "missing") },
	} {
		err := operation()
		require.ErrorIs(t, err, sql.ErrNoRows)
		// Both the update and delete service paths can add generic DB context.
		status, _ := ierr.ResolveError(ierr.WithError(err).Mark(ierr.ErrDatabase))
		require.Equal(t, http.StatusNotFound, status)
	}
}

func TestDomainHTTPInvalidPlanSort(t *testing.T) {
	log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
	// No SQL driver is attached: rejecting an unknown field needs no I/O.
	repo := repository.NewPlanRepository(domainReadClient{client: ent.NewClient()}, log, nil)
	svc := service.NewPlanService(service.ServiceParams{PlanRepo: repo, Logger: log})
	handler := v1.NewPlanHandler(svc, nil, nil, nil, nil, nil, log)
	filter := types.NewPlanFilter()
	filter.Sort = []*types.SortCondition{{Field: "not_a_plan_column", Direction: types.SortDirectionAsc}}
	_, err := repo.List(context.Background(), filter)
	require.True(t, ierr.IsValidation(err), "the real repository rejects the unknown field before I/O")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.POST("/plans/search", handler.QueryPlans)
	req := httptest.NewRequest(http.MethodPost, "/plans/search", strings.NewReader(`{"limit":50,"sort":[{"field":"not_a_plan_column","direction":"asc"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	t.Logf("status=%d retry=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	require.Equal(t, 400, w.Code)
	require.Empty(t, w.Header().Get("Retry-After"))
}

func TestDomainHTTPRepositoryAbsenceCauses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cause  error
		status int
	}{
		{"real absence", nil, 404},
		{"disk full", &pq.Error{Code: "53100"}, 503},
		{"unknown DB error", errors.New("synthetic unknown database failure"), 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := sql.OpenDB(domainEmptyConnector{queryErr: tt.cause})
			t.Cleanup(func() { _ = db.Close() })
			client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			read := domainReadClient{client: client}
			log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
			sub := repository.NewSubscriptionRepository(read, log, domainMissCache{})
			cs := repository.NewCostsheetRepository(read, log)
			phase := repository.NewSubscriptionPhaseRepository(read, log)
			mapping := repository.NewEntityIntegrationMappingRepository(read, log, domainMissCache{})
			grant := repository.NewCreditGrantApplicationRepository(read, log, domainMissCache{})
			wallet := repository.NewWalletRepository(read, log, domainMissCache{})
			ctx := types.SetTenantID(types.SetEnvironmentID(context.Background(), "environment"), "tenant")
			operations := map[string]func() error{
				"subscription get":         func() error { _, err := sub.Get(ctx, "missing"); return err },
				"subscription lock":        func() error { _, err := sub.GetForUpdate(ctx, "missing"); return err },
				"subscription with items":  func() error { _, _, err := sub.GetWithLineItems(ctx, "missing"); return err },
				"costsheet ID":             func() error { _, err := cs.GetByID(ctx, "missing"); return err },
				"costsheet name":           func() error { _, err := cs.GetByName(ctx, "tenant", "environment", "missing"); return err },
				"costsheet key":            func() error { _, err := cs.GetByLookupKey(ctx, "tenant", "environment", "missing"); return err },
				"subscription phase":       func() error { _, err := phase.Get(ctx, "missing"); return err },
				"integration mapping":      func() error { _, err := mapping.Get(ctx, "missing"); return err },
				"credit grant idempotency": func() error { _, err := grant.FindByIdempotencyKey(ctx, "missing"); return err },
				"wallet status zero rows":  func() error { return wallet.UpdateWalletStatus(ctx, "missing", types.WalletStatusClosed) },
			}
			for name, operation := range operations {
				t.Run(name, func(t *testing.T) {
					err := operation()
					require.Error(t, err)
					if tt.cause == nil {
						var absence *ent.NotFoundError
						require.True(t, errors.As(err, &absence) || errors.Is(err, sql.ErrNoRows), "retain actual empty-query evidence")
					} else {
						require.ErrorIs(t, err, tt.cause)
						require.False(t, ierr.IsNotFound(err))
					}
					status, _ := ierr.ResolveError(ierr.WithError(err).Mark(ierr.ErrDatabase))
					require.Equal(t, tt.status, status)
				})
			}
		})
	}
}

func TestDomainHTTPRepositoryOutageControls(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cause  error
		status int
	}{
		{"disk full", &pq.Error{Code: "53100", Message: "private driver text"}, 503},
		{"unknown database failure", errors.New("private unknown driver text"), 500},
		{"server internal failure", &pq.Error{Code: "XX000", Message: "private driver text"}, 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var queries atomic.Int32
			db := sql.OpenDB(domainEmptyConnector{queryErr: tt.cause, queries: &queries})
			t.Cleanup(func() { _ = db.Close() })
			client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
			read := domainReadClient{client: client}
			taskSvc := service.NewScheduledTaskService(repository.NewScheduledTaskRepository(read, log), nil, domainUnusedTemporal{}, log, nil)
			taskHandler := v1.NewScheduledTaskHandler(taskSvc, log)
			planSvc := service.NewPlanService(service.ServiceParams{PlanRepo: repository.NewPlanRepository(read, log, nil), Logger: log})
			planHandler := v1.NewPlanHandler(planSvc, nil, nil, nil, nil, nil, log)
			r := gin.New()
			r.Use(middleware.ErrorHandler())
			r.GET("/tasks/scheduled/:id", taskHandler.GetScheduledTask)
			r.POST("/tasks/scheduled/:id/run", taskHandler.TriggerForceRun)
			r.POST("/plans/search", planHandler.QueryPlans)
			for _, request := range []struct{ method, path, body string }{
				{http.MethodGet, "/tasks/scheduled/task", ""},
				{http.MethodPost, "/tasks/scheduled/task/run", ""},
				{http.MethodPost, "/plans/search", `{"limit":50,"sort":[{"field":"name","direction":"asc"}]}`},
			} {
				before := queries.Load()
				w := httptest.NewRecorder()
				req := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
				req.Header.Set("Content-Type", "application/json")
				r.ServeHTTP(w, req)
				require.Greater(t, queries.Load(), before, "control must reach the real Ent SQL boundary")
				require.Equal(t, tt.status, w.Code, request.path)
				require.NotContains(t, w.Body.String(), "private")
				if tt.status == http.StatusServiceUnavailable {
					require.Equal(t, "5", w.Header().Get("Retry-After"))
				} else {
					require.Empty(t, w.Header().Get("Retry-After"))
				}
			}
		})
	}
}
