package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPostgresConnectionTimeout(t *testing.T) {
	for _, seconds := range []int{0, -1, 4} {
		cfg := PostgresConfig{ConnectTimeoutSeconds: seconds}
		expected := "connect_timeout=2"
		if seconds == 4 {
			expected = "connect_timeout=4"
		}
		require.Contains(t, cfg.GetDSN(), expected)
		require.Contains(t, cfg.GetReaderDSN(), expected)
	}
}
