package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRillvenEnvironmentPrecedence(t *testing.T) {
	t.Setenv("RENEWAPI_UPSTREAM_CONCURRENCY", "8")
	t.Setenv("RILLVEN_UPSTREAM_CONCURRENCY", "")
	require.Equal(t, 8, GetEnvOrDefault("RILLVEN_UPSTREAM_CONCURRENCY", 4))
	t.Setenv("RILLVEN_UPSTREAM_CONCURRENCY", "12")
	require.Equal(t, 12, GetEnvOrDefault("RILLVEN_UPSTREAM_CONCURRENCY", 4))
	// An invalid canonical value must not silently enable the legacy setting.
	t.Setenv("RILLVEN_UPSTREAM_CONCURRENCY", "invalid")
	require.Equal(t, 4, GetEnvOrDefault("RILLVEN_UPSTREAM_CONCURRENCY", 4))
	t.Setenv("RENEWAPI_UPSTREAM_CONCURRENCY", "")
	t.Setenv("RILLVEN_UPSTREAM_CONCURRENCY", "")
	require.Equal(t, 4, GetEnvOrDefault("RILLVEN_UPSTREAM_CONCURRENCY", 4))
}
