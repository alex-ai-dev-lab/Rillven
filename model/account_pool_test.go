package model

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAccountPoolModelTest(t *testing.T) billingLedgerFixture {
	t.Helper()
	fixture := setupBillingLedgerTest(t)
	tables := []any{&UpstreamAccount{}, &ChannelAccountBinding{}, &AccountCorrelation{}, &AccountContextSequence{}, &AccountContextAllocation{}, &UpstreamCredential{}, &UpstreamCredentialAlias{}, &AccountQuotaWindow{}, &AccountProbeLease{}, &ConversationRequest{}, &ConversationDeletion{}, &ConversationResource{}, &ConversationCheckpoint{}, &ConversationTurnCommit{}, &ConversationSession{}, &ConversationOwnerUsage{}}
	require.NoError(t, DB.AutoMigrate(tables...))
	db := DB
	t.Cleanup(func() {
		if db.Dialector.Name() != "sqlite" {
			require.NoError(t, db.Migrator().DropTable(tables...))
		}
	})
	return fixture
}

func TestAccountConcurrentProvisioningAndClaimsChooseOneWinner(t *testing.T) {
	setupAccountPoolModelTest(t)
	ring, err := common.Marshal(map[string]string{"test": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	require.NoError(t, err)
	t.Setenv("RILLVEN_STATE_KEYS", string(ring))
	t.Setenv("RILLVEN_STATE_ACTIVE_KEY", "test")
	start := make(chan struct{})
	var workers sync.WaitGroup
	var winners atomic.Int32
	type result struct {
		account, installation, correlation, response string
		err                                          error
	}
	results := make(chan result, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			ctx := context.Background()
			subject, err := StableCredentialSubject(ctx, "opaque-shared")
			if err != nil {
				results <- result{err: err}
				return
			}
			a, err := EnsureUpstreamAccount(ctx, "codex", subject, AccountIdentitySecret{Seed: base64.StdEncoding.EncodeToString(make([]byte, 32)), LegacySlot: uuid.NewString(), LegacyRevision: 1, NewInstallation: true}, "", false)
			if err != nil {
				results <- result{err: err}
				return
			}
			secret, err := a.Secret()
			if err != nil {
				results <- result{err: err}
				return
			}
			correlation, err := ResolveAccountCorrelation(ctx, a.ID, "owner", credentialDigest("thread"))
			if err != nil {
				results <- result{err: err}
				return
			}
			claim, created, err := ClaimConversationRequest(ctx, "owner", credentialDigest("request"), "payload", "resp_")
			if created && err == nil {
				winners.Add(1)
			}
			results <- result{a.ID, secret.InstallationID, correlation, claim.ResponseID, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var winner result
	for got := range results {
		require.NoError(t, got.err)
		if winner.account == "" {
			winner = got
		}
		require.Equal(t, winner, got)
	}
	require.EqualValues(t, 1, winners.Load())
	var credentials int64
	require.NoError(t, DB.Model(&UpstreamCredential{}).Count(&credentials).Error)
	require.EqualValues(t, 1, credentials, "concurrent aliases retain only their shared credential winner")
	_, err = ResolveAccountCorrelation(t.Context(), winner.account, "another-owner", credentialDigest("thread"))
	require.Error(t, err, "a reused digest cannot cross owners")
}

func TestAccountQuotaExhaustionCannotBeRevivedByLateSuccess(t *testing.T) {
	setupAccountPoolModelTest(t)
	require.NoError(t, MigrateAccountQuota())
	ctx := context.Background()
	now, err := DatabaseMillis(ctx)
	require.NoError(t, err)
	row := AccountQuotaWindow{AccountID: "a", Window: "primary", Remaining: .5, ObservedAt: now, ResetAt: now + 3600000, Source: "test"}
	require.NoError(t, StoreAccountQuota(ctx, []AccountQuotaWindow{row}))
	row.Remaining = 0
	require.NoError(t, StoreAccountQuota(ctx, []AccountQuotaWindow{row}))
	row.Remaining, row.ObservedAt = .9, now+100
	require.NoError(t, StoreAccountQuota(ctx, []AccountQuotaWindow{row}))
	var stored AccountQuotaWindow
	require.NoError(t, DB.First(&stored).Error)
	require.Zero(t, stored.Remaining)
	row.ResetAt += 3600000
	require.NoError(t, StoreAccountQuota(ctx, []AccountQuotaWindow{row}))
	require.NoError(t, DB.First(&stored).Error)
	require.Equal(t, .9, stored.Remaining)
}

func TestAccountExpiredExhaustionUsesHalfOpenAdmission(t *testing.T) {
	setupAccountPoolModelTest(t)
	account := UpstreamAccount{ID: uuid.NewString(), Provider: "codex", SubjectHash: "subject", Revision: 1, Ciphertext: "fixture"}
	require.NoError(t, DB.Create(&account).Error)
	now, err := DatabaseMillis(t.Context())
	require.NoError(t, err)
	window := AccountQuotaWindow{AccountID: account.ID, Window: "primary", Remaining: 0, ResetAt: now - 1000, ObservedAt: now - 2000, Source: "test"}
	require.NoError(t, StoreAccountQuota(t.Context(), []AccountQuotaWindow{window}))
	halfOpen, err := AccountQuotaHalfOpen(t.Context(), "subject")
	require.NoError(t, err)
	require.True(t, halfOpen)
	window.Remaining, window.ResetAt, window.ObservedAt = .5, now+3600000, now
	require.NoError(t, StoreAccountQuota(t.Context(), []AccountQuotaWindow{window}))
	halfOpen, err = AccountQuotaHalfOpen(t.Context(), "subject")
	require.NoError(t, err)
	require.False(t, halfOpen)
}

func TestAccountUpgradePreservesLegacyMigrationAndCiphertext(t *testing.T) {
	setupAccountPoolModelTest(t)
	require.NoError(t, DB.Migrator().DropColumn(&ConversationCheckpoint{}, "AccountID"))
	require.NoError(t, DB.Migrator().DropTable(&ConversationDeletion{}))
	require.NoError(t, DB.AutoMigrate(&SchemaMigration{}))
	t.Cleanup(func() { require.NoError(t, DB.Migrator().DropTable(&SchemaMigration{})) })
	var legacy, upgrade schemaMigrationDefinition
	for _, def := range mainSchemaMigrationDefinitions() {
		if def.Key == "conversation-state:v1" {
			legacy = def
		}
		if def.Key == "conversation-account:v1" {
			upgrade = def
		}
	}
	require.Equal(t, "2026-11-15.1", legacy.Revision)
	row := SchemaMigration{Key: legacy.Key, Checksum: schemaMigrationChecksum(legacy), AppVersion: "v1.6.0", AppliedAt: 1}
	require.NoError(t, DB.Create(&row).Error)
	require.NoError(t, DB.Exec("INSERT INTO conversation_checkpoints (id, owner, session_id, channel_id, slot_id, route_key, state, ciphertext, content_hash, bytes, depth, portable, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", "legacy-response", "owner", "session", 1, "slot", "route", "published", "old-encrypted-payload", "hash", 21, 1, true, 1).Error)
	require.NoError(t, runSchemaMigration(legacy), "the already applied migration remains untouched")
	require.False(t, DB.Migrator().HasColumn(&ConversationCheckpoint{}, "AccountID"))
	require.NoError(t, runSchemaMigration(upgrade))
	require.NoError(t, runSchemaMigration(upgrade), "the additive migration can run twice")
	require.True(t, DB.Migrator().HasTable(&ConversationDeletion{}))
	var checkpoint ConversationCheckpoint
	require.NoError(t, DB.First(&checkpoint, "id = ?", "legacy-response").Error)
	require.Empty(t, checkpoint.AccountID)
	require.Equal(t, StateBlob("old-encrypted-payload"), checkpoint.Ciphertext)
}

func TestAccountProbeAndIdempotencyClaimsAreShared(t *testing.T) {
	setupAccountPoolModelTest(t)
	require.NoError(t, MigrateAccountQuota())
	require.NoError(t, MigrateConversationState())
	ctx := context.Background()
	claimed, err := ClaimAccountProbe(ctx, "a", "instance-one", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = ClaimAccountProbe(ctx, "a", "instance-two", time.Minute)
	require.NoError(t, err)
	require.False(t, claimed)
	first, created, err := ClaimConversationRequest(ctx, "one", "key-one", "hash", "resp_")
	require.NoError(t, err)
	require.True(t, created)
	second, created, err := ClaimConversationRequest(ctx, "one", "key-one", "hash", "resp_")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ResponseID, second.ResponseID)
	_, _, err = ClaimConversationRequest(ctx, "one", "key-one", "changed", "resp_")
	require.ErrorIs(t, err, ErrConversationRequestConflict)
}

func TestAccountBillingLockRetriesRollbackBalances(t *testing.T) {
	fixture := setupBillingLedgerTest(t)
	reservation := reserveWalletLedger(t, fixture, "lock-retry", 100)
	attempts := 0
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("test:billing-lock", func(tx *gorm.DB) {
		if tx.Statement.Table == "billing_ledgers" && attempts < 2 {
			attempts++
			tx.AddError(errors.New("database is locked (SQLITE_BUSY)"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove("test:billing-lock") })
	_, err := SettleBillingLedger(reservation.Ledger.ID, 200)
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	assertLedgerBalances(t, fixture, 800, 200, 200, 200)
	_, err = SettleBillingLedger(reservation.Ledger.ID, 200)
	require.NoError(t, err)
	assertLedgerBalances(t, fixture, 800, 200, 200, 200)
}

func TestAccountEncryptedVaultAndAllocationPersistAcrossConnections(t *testing.T) {
	setupAccountPoolModelTest(t)
	ring, err := common.Marshal(map[string]string{"test": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	require.NoError(t, err)
	t.Setenv("RILLVEN_STATE_KEYS", string(ring))
	t.Setenv("RILLVEN_STATE_ACTIVE_KEY", "test")
	ctx := context.Background()
	setting := `{"client_identity_mode":"per_account"}`
	channel := Channel{Name: "pool", Key: "old-material", Setting: &setting, Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	subject, err := StableCredentialSubject(ctx, channel.Key)
	require.NoError(t, err)
	account, err := EnsureUpstreamAccount(ctx, "codex", subject, AccountIdentitySecret{Seed: base64.StdEncoding.EncodeToString(make([]byte, 32)), LegacySlot: uuid.NewString(), LegacyRevision: 1, InstallationID: uuid.NewString(), DeviceID: "stable-device", UserAgent: "test-agent"}, "", false)
	require.NoError(t, err)
	correlation, err := ResolveAccountCorrelation(ctx, account.ID, "owner", credentialDigest("thread"))
	require.NoError(t, err)
	allocation, err := ResolveAccountContextAllocation(ctx, account.ID, "owner", credentialDigest("sequence"), credentialDigest("window"), 42)
	require.NoError(t, err)
	require.NoError(t, ApplyCodexCredentialCAS(ctx, channel.Id, "old-material", "new-material", channel.ConfigVersion))
	copy, err := GetChannelByIDTx(DB.Session(&gorm.Session{NewDB: true}), channel.Id)
	require.NoError(t, err)
	require.Equal(t, "new-material", copy.Key)
	rotated, err := StableCredentialSubject(ctx, copy.Key)
	require.NoError(t, err)
	require.Equal(t, subject, rotated)
	restored, err := ResolveAccountCorrelation(ctx, account.ID, "owner", credentialDigest("thread"))
	require.NoError(t, err)
	require.Equal(t, correlation, restored)
	restoredWindow, err := ResolveAccountContextAllocation(ctx, account.ID, "owner", credentialDigest("sequence"), credentialDigest("window"), 99)
	require.NoError(t, err)
	require.Equal(t, allocation, restoredWindow)
	secret, err := account.Secret()
	require.NoError(t, err)
	require.Equal(t, "stable-device", secret.DeviceID)
}
