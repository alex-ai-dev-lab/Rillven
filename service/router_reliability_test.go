package service

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func resetRouterCooldownTrackerForTest() {
	routerCooldownTracker.Lock()
	defer routerCooldownTracker.Unlock()
	routerCooldownTracker.items = make(map[string]RouterCooldownState)
}

func cooldownStateForTest(channelID int, info *relaycommon.RelayInfo) (RouterCooldownState, bool) {
	routerCooldownTracker.Lock()
	defer routerCooldownTracker.Unlock()
	state, ok := routerCooldownTracker.items[routerCooldownKey(channelID, info)]
	return state, ok
}

func TestRouterCooldownDisabledDoesNotExclude(t *testing.T) {
	t.Setenv("ROUTER_COOLDOWN_ENABLED", "false")
	resetRouterCooldownTrackerForTest()

	info := &relaycommon.RelayInfo{OriginModelName: "gpt-test", RelayMode: 1}
	err := types.NewOpenAIError(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	RecordRouterCooldownFailure(10, info, err)
	RecordRouterCooldownFailure(10, info, err)

	param := &RetryParam{ExcludedChannelIds: map[int]bool{}}
	ApplyRouterCooldownFilter(info, param)
	require.False(t, param.ExcludedChannelIds[10])
}

func TestRouterCooldownExcludesAfterThresholdAndClears(t *testing.T) {
	t.Setenv("ROUTER_COOLDOWN_ENABLED", "true")
	t.Setenv("ROUTER_COOLDOWN_THRESHOLD", "2")
	t.Setenv("ROUTER_COOLDOWN_SECONDS", "60")
	resetRouterCooldownTrackerForTest()

	info := &relaycommon.RelayInfo{OriginModelName: "gpt-test", RelayMode: 1}
	err := types.NewOpenAIError(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	param := &RetryParam{ExcludedChannelIds: map[int]bool{}}
	RecordRouterCooldownFailure(10, info, err)
	ApplyRouterCooldownFilter(info, param)
	require.False(t, param.ExcludedChannelIds[10])

	RecordRouterCooldownFailure(10, info, err)
	ApplyRouterCooldownFilter(info, param)
	require.True(t, param.ExcludedChannelIds[10])

	delete(param.ExcludedChannelIds, 10)
	ClearRouterCooldown(10, info)
	ApplyRouterCooldownFilter(info, param)
	require.False(t, param.ExcludedChannelIds[10])
}

func TestRouterCooldownUsesBoundedRetryHint(t *testing.T) {
	t.Setenv("ROUTER_COOLDOWN_ENABLED", "true")
	t.Setenv("ROUTER_COOLDOWN_THRESHOLD", "1")
	t.Setenv("ROUTER_COOLDOWN_SECONDS", "45")
	resetRouterCooldownTrackerForTest()

	info := &relaycommon.RelayInfo{OriginModelName: "gpt-test", RelayMode: 1}
	err := types.NewOpenAIError(
		errors.New("rate limited"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusTooManyRequests,
		types.ErrOptionWithRetryHint(2*time.Minute),
	)
	RecordRouterCooldownFailure(10, info, err)

	state, ok := cooldownStateForTest(10, info)
	require.True(t, ok)
	require.Equal(t, 2*time.Minute, state.CooldownUntil.Sub(state.LastFailure))
}

func TestRouterCooldownLateSuccessCannotClearNewerFailure(t *testing.T) {
	t.Setenv("ROUTER_COOLDOWN_ENABLED", "true")
	t.Setenv("ROUTER_COOLDOWN_THRESHOLD", "1")
	resetRouterCooldownTrackerForTest()
	info := &relaycommon.RelayInfo{OriginModelName: "test", RelayMode: 1, AttemptStartedAt: time.Now().Add(-time.Minute)}
	failure := types.NewOpenAIError(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests, types.ErrOptionWithRetryHint(time.Hour))
	RecordRouterCooldownFailure(10, info, failure)
	first, exists := cooldownStateForTest(10, info)
	require.True(t, exists)
	ClearRouterCooldown(10, info)
	current, exists := cooldownStateForTest(10, info)
	require.True(t, exists)
	require.Equal(t, first.CooldownUntil, current.CooldownUntil)
	failure.RetryHint = time.Minute
	RecordRouterCooldownFailure(10, info, failure)
	current, _ = cooldownStateForTest(10, info)
	require.Equal(t, first.CooldownUntil, current.CooldownUntil)
	info.AttemptStartedAt = current.LastFailure.Add(time.Second)
	ClearRouterCooldown(10, info)
	_, exists = cooldownStateForTest(10, info)
	require.False(t, exists)
}

func TestRouterCooldownFallsBackToConfiguredTTL(t *testing.T) {
	t.Setenv("ROUTER_COOLDOWN_ENABLED", "true")
	t.Setenv("ROUTER_COOLDOWN_THRESHOLD", "1")
	t.Setenv("ROUTER_COOLDOWN_SECONDS", "45")
	resetRouterCooldownTrackerForTest()

	info := &relaycommon.RelayInfo{OriginModelName: "gpt-test", RelayMode: 1}
	err := types.NewOpenAIError(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	RecordRouterCooldownFailure(10, info, err)

	state, ok := cooldownStateForTest(10, info)
	require.True(t, ok)
	require.Equal(t, 45*time.Second, state.CooldownUntil.Sub(state.LastFailure))
}

func TestRouterCooldownIgnoresNonRetryableFailures(t *testing.T) {
	t.Setenv("ROUTER_COOLDOWN_ENABLED", "true")
	resetRouterCooldownTrackerForTest()
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-test", RelayMode: 1}

	tests := []struct {
		name string
		err  *types.NewAPIError
	}{
		{
			name: "skip retry",
			err:  types.NewOpenAIError(errors.New("skip"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway, types.ErrOptionWithSkipRetry()),
		},
		{
			name: "client canceled",
			err:  types.NewError(context.Canceled, types.ErrorCodeDoRequestFailed),
		},
		{
			name: "tls verification",
			err:  types.NewError(&x509.UnknownAuthorityError{}, types.ErrorCodeDoRequestFailed),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetRouterCooldownTrackerForTest()
			RecordRouterCooldownFailure(10, info, test.err)
			_, ok := cooldownStateForTest(10, info)
			require.False(t, ok)
		})
	}
}

func TestRouterRetryBackoffDelayRange(t *testing.T) {
	t.Setenv("ROUTER_RETRY_BACKOFF_BASE_MS", "100")
	t.Setenv("ROUTER_RETRY_BACKOFF_MAX_MS", "1000")
	oldRelayTimeout := common.RelayTimeout
	common.RelayTimeout = 0
	defer func() {
		common.RelayTimeout = oldRelayTimeout
	}()

	for i := 0; i < 20; i++ {
		delay := RouterRetryBackoffDelay(2)
		require.GreaterOrEqual(t, delay, 100*time.Millisecond)
		require.LessOrEqual(t, delay, 300*time.Millisecond)
	}
}
