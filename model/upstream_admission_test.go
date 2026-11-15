package model

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamAdmissionFIFOLeaseAndCooldown(t *testing.T) {
	setupBillingLedgerTest(t)
	tables := []any{&UpstreamAdmission{}, &UpstreamPoolState{}}
	require.NoError(t, DB.Migrator().DropTable(tables...))
	require.NoError(t, MigrateUpstreamAdmission())
	t.Cleanup(func() { _ = DB.Migrator().DropTable(tables...) })
	ctx := context.Background()
	first, err := QueueUpstreamAdmission(ctx, "account", "a", 2, time.Second)
	require.NoError(t, err)
	second, err := QueueUpstreamAdmission(ctx, "account", "b", 2, time.Second)
	require.NoError(t, err)
	_, err = QueueUpstreamAdmission(ctx, "account", "c", 2, time.Second)
	require.ErrorIs(t, err, ErrUpstreamAdmissionBusy)
	active, err := TryActivateUpstreamAdmission(ctx, second, 1, time.Minute)
	require.NoError(t, err)
	require.False(t, active, "later waiter must not jump the queue")
	active, err = TryActivateUpstreamAdmission(ctx, first, 1, time.Minute)
	require.NoError(t, err)
	require.True(t, active)
	active, err = TryActivateUpstreamAdmission(ctx, second, 1, time.Minute)
	require.NoError(t, err)
	require.False(t, active)
	require.NoError(t, RenewUpstreamAdmission(ctx, first, 2*time.Minute))
	require.NoError(t, ReleaseUpstreamAdmission(ctx, first))
	active, err = TryActivateUpstreamAdmission(ctx, second, 1, time.Minute)
	require.NoError(t, err)
	require.True(t, active)
	require.ErrorIs(t, RenewUpstreamAdmission(ctx, first, time.Minute), ErrUpstreamAdmissionBusy)
	long := time.Now().Add(time.Hour).UnixMilli()
	require.NoError(t, BlockUpstreamScope(ctx, "account/model", long))
	require.NoError(t, BlockUpstreamScope(ctx, "account/model", time.Now().Add(time.Minute).UnixMilli()))
	until, err := UpstreamBlockedUntil(ctx, []string{"account", "account/model"})
	require.NoError(t, err)
	require.Equal(t, long, until)
	until, err = UpstreamBlockedUntil(ctx, []string{"other-account", "other-account/model"})
	require.NoError(t, err)
	require.Zero(t, until)
	require.NoError(t, ReleaseUpstreamAdmission(ctx, second))
	var count int64
	require.NoError(t, DB.Model(&UpstreamAdmission{}).Count(&count).Error)
	require.Zero(t, count)
}
