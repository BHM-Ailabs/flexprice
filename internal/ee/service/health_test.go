package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadiness(t *testing.T) {
	ok := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("dependency down") }
	hung := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	for _, tt := range []struct {
		name   string
		checks []func(context.Context) error
		want   bool
	}{
		{"healthy", []func(context.Context) error{ok, ok, ok, ok}, true},
		{"postgres down", []func(context.Context) error{down, ok, ok, ok}, false},
		{"other dependency down", []func(context.Context) error{ok, ok, down, ok}, false},
		{"deadline", []func(context.Context) error{hung, ok, hung}, false},
		{"no checks", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &healthService{checks: tt.checks, timeout: 20 * time.Millisecond}
			start := time.Now()
			require.Equal(t, tt.want, svc.Ready(context.Background()))
			require.Less(t, time.Since(start), time.Second)
		})
	}
}
