package service

import (
	"context"
	"fmt"
	"time"

	"github.com/flexprice/flexprice/internal/clickhouse"
	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/kafka"
	"github.com/flexprice/flexprice/internal/postgres"
	temporalservice "github.com/flexprice/flexprice/internal/temporal/service"
)

const readinessTimeout = 2 * time.Second

type HealthService interface {
	Ready(context.Context) bool
}

type healthService struct {
	checks  []func(context.Context) error
	timeout time.Duration
}

func NewHealthService(cfg *config.Configuration, db *postgres.EntClients, ch *clickhouse.ClickHouseStore, temporal temporalservice.TemporalService) HealthService {
	checks := []func(context.Context) error{
		db.Ping,
		func(ctx context.Context) error { return ch.GetRawConn().Ping(ctx) },
		func(ctx context.Context) error { return kafka.CheckReadiness(ctx, &cfg.Kafka) },
	}
	if cfg.Temporal.Address != "" {
		checks = append(checks, func(ctx context.Context) error {
			if temporal == nil || !temporal.IsHealthy(ctx) {
				return fmt.Errorf("temporal unavailable")
			}
			return nil
		})
	}
	return &healthService{checks: checks, timeout: readinessTimeout}
}

func (s *healthService) Ready(ctx context.Context) bool {
	if s == nil || len(s.checks) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	results := make(chan error, len(s.checks))
	for _, check := range s.checks {
		go func(check func(context.Context) error) { results <- check(ctx) }(check)
	}
	for range s.checks {
		select {
		case err := <-results:
			if err != nil {
				return false
			}
		case <-ctx.Done():
			return false
		}
	}
	return ctx.Err() == nil
}
