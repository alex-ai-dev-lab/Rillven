package model

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestConversationDurableReceiptAndLargeState(t *testing.T) {
	fixture := setupBillingLedgerTest(t)
	tables := []any{&ConversationResource{}, &ConversationTurnCommit{}, &ConversationCheckpoint{}, &ConversationSession{}, &ConversationOwnerUsage{}, &CodexRefreshJournal{}}
	require.NoError(t, DB.Migrator().DropTable(tables...))
	require.NoError(t, MigrateConversationState())
	require.NoError(t, DB.AutoMigrate(&CodexRefreshJournal{}))
	t.Cleanup(func() { _ = DB.Migrator().DropTable(tables...) })
	ctx := context.Background()
	ledger := reserveWalletLedger(t, fixture, "durable-turn", 40)
	now := time.Now().Unix()
	ciphertext := strings.Repeat("encrypted-state", 160000)
	input := ConversationCommitInput{
		Session:       ConversationSession{ID: "s", Owner: "o", ExpiresAt: now + 300, DeadlineAt: now + 300, ActiveUntil: now + 600},
		Checkpoint:    ConversationCheckpoint{ID: "r", Owner: "o", SessionID: "s", ChannelID: fixture.channel.Id, State: "prepared", Ciphertext: StateBlob(ciphertext), ContentHash: "hash", Bytes: int64(len(ciphertext)), Depth: 1},
		Receipt:       ConversationTurnCommit{RequestID: "durable-turn", Owner: "o", SessionID: "s", CheckpointID: "r", ChannelID: fixture.channel.Id, BillingLedgerID: ledger.Ledger.ID, ActualQuota: 25, State: "prepared"},
		MaxOwnerBytes: 8 << 20, MaxSessionBytes: 4 << 20,
	}
	require.NoError(t, PrepareConversationCommit(ctx, input))
	require.NoError(t, PrepareConversationCommit(ctx, input), "prepare is idempotent")
	require.Error(t, PublishConversationCommit(ctx, "o", "durable-turn"), "cannot publish unpaid state")
	stored, err := GetBillingLedger(ledger.Ledger.ID)
	require.NoError(t, err)
	require.Equal(t, BillingLedgerDesiredSettle, stored.DesiredState)
	require.EqualValues(t, 25, stored.DesiredQuota)
	// Simulate restart: complete only the durable intent, without calling upstream.
	_, err = SettleBillingLedger(stored.ID, stored.DesiredQuota, stored.ChannelID)
	require.NoError(t, err)
	require.NoError(t, ReconcileConversationCommits(ctx, 10))
	checkpoint, err := FindConversationCheckpoint(ctx, "o", "r")
	require.NoError(t, err)
	require.Equal(t, ciphertext, string(checkpoint.Ciphertext))
	require.NoError(t, ReconcileConversationCommits(ctx, 10))
	assertLedgerBalances(t, fixture, 975, 25, 25, 25)
	_, err = FindConversationCheckpoint(ctx, "other", "r")
	require.ErrorIs(t, err, ErrConversationMissing)
	input.Checkpoint.ContentHash = "conflict"
	require.Error(t, PrepareConversationCommit(ctx, input))
	input.Checkpoint.ID, input.Receipt.CheckpointID, input.Receipt.RequestID = "too-large", "too-large", "another"
	input.Receipt.BillingLedgerID, input.Receipt.ActualQuota = 0, 0
	input.MaxOwnerBytes = int64(len(ciphertext))
	require.ErrorIs(t, PrepareConversationCommit(ctx, input), ErrConversationLimit)
	var usage ConversationOwnerUsage
	require.NoError(t, DB.First(&usage, "owner = ?", "o").Error)
	require.EqualValues(t, len(ciphertext), usage.Bytes, "failed admission must roll back byte accounting")
}

func TestManagedStateIdentityAndRefreshPersistence(t *testing.T) {
	fixture := setupBillingLedgerTest(t)
	keys, err := common.Marshal(map[string]string{"qa": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	require.NoError(t, err)
	t.Setenv("RENEWAPI_STATE_KEYS", string(keys))
	t.Setenv("RENEWAPI_STATE_ACTIVE_KEY", "qa")
	tables := []any{&ChannelIdentity{}, &ChannelCredentialSlot{}, &CodexRefreshJournal{}}
	require.NoError(t, DB.Migrator().DropTable(tables...))
	require.NoError(t, DB.AutoMigrate(tables...))
	t.Cleanup(func() { _ = DB.Migrator().DropTable(tables...) })
	ctx := context.Background()
	identity, err := EnsureChannelIdentity(ctx, fixture.channel.Id)
	require.NoError(t, err)
	secret, err := identity.Secret()
	require.NoError(t, err)
	require.NotContains(t, identity.Ciphertext, secret.Seed)
	again, err := EnsureChannelIdentity(ctx, fixture.channel.Id)
	require.NoError(t, err)
	require.Equal(t, identity, again)
	slot, err := EnsureChannelCredentialSlot(ctx, fixture.channel.Id, "subject")
	require.NoError(t, err)
	sameSlot, err := EnsureChannelCredentialSlot(ctx, fixture.channel.Id, "subject")
	require.NoError(t, err)
	require.Equal(t, slot.ID, sameSlot.ID)
	_, err = ClaimCodexRefresh(ctx, "grant", "worker-a")
	require.NoError(t, err)
	_, err = ClaimCodexRefresh(ctx, "grant", "worker-b")
	require.ErrorIs(t, err, ErrRefreshGrantUnavailable)
	require.NoError(t, MarkCodexRefreshSent(ctx, "grant", "worker-a"))
	require.NoError(t, RecordCodexRefreshResult(ctx, "grant", "worker-a", "received", "encrypted-receipt"))
	receipt, err := ClaimCodexRefresh(ctx, "grant", "worker-b")
	require.NoError(t, err)
	require.Equal(t, StateBlob("encrypted-receipt"), receipt.Ciphertext)
	require.NoError(t, ApplyCodexCredentialCAS(ctx, fixture.channel.Id, fixture.channel.Key, "rotated", fixture.channel.ConfigVersion))
	require.Error(t, ApplyCodexCredentialCAS(ctx, fixture.channel.Id, fixture.channel.Key, "stale", fixture.channel.ConfigVersion))
}
