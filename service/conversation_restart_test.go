package service

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestManagedConversationSurvivesProcessRestart(t *testing.T) {
	for _, mode := range []string{BillingLedgerModeOff, BillingLedgerModeShadow, BillingLedgerModeEnforce} {
		for _, source := range []string{BillingSourceWallet, BillingSourceSubscription} {
			for _, stage := range []string{"before_prepare", "prepared", "settled", "published", "delivered"} {
				t.Run(mode+"/"+source+"/"+stage, func(t *testing.T) {
					dsn := filepath.Join(t.TempDir(), "conversation.db")
					binary, err := os.Executable()
					require.NoError(t, err)
					run := func(phase string) ([]byte, error) {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						command := exec.CommandContext(ctx, binary, "-test.run=^TestManagedConversationRestartWorker$", "-test.v")
						for _, entry := range os.Environ() {
							key := strings.SplitN(entry, "=", 2)[0]
							if strings.HasPrefix(key, "BILLING_TEST_") || key == "BILLING_LEDGER_MODE" || strings.HasPrefix(key, "RENEWAPI_RESTART_") {
								continue
							}
							command.Env = append(command.Env, entry)
						}
						command.Env = append(command.Env, "BILLING_TEST_DRIVER=sqlite", "BILLING_TEST_DSN="+dsn,
							"BILLING_LEDGER_MODE="+mode, "RENEWAPI_RESTART_PHASE="+phase, "RENEWAPI_RESTART_SCENARIO="+stage, "RENEWAPI_RESTART_SOURCE="+source)
						return command.CombinedOutput()
					}
					output, err := run("crash")
					var exitError *exec.ExitError
					require.ErrorAs(t, err, &exitError, string(output))
					require.Equal(t, 23, exitError.ExitCode(), string(output))
					output, err = run("recover")
					require.NoError(t, err, string(output))
				})
			}
		}
	}
}

// The worker exits without defers so only committed database state survives.
func TestManagedConversationRestartWorker(t *testing.T) {
	phase := os.Getenv("RENEWAPI_RESTART_PHASE")
	if phase == "" {
		t.Skip("invoked by the process restart test")
	}
	keys, err := common.Marshal(map[string]string{"qa": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	require.NoError(t, err)
	t.Setenv("RENEWAPI_STATE_KEYS", string(keys))
	t.Setenv("RENEWAPI_STATE_ACTIVE_KEY", "qa")
	stage := os.Getenv("RENEWAPI_RESTART_SCENARIO")
	ctx := context.Background()
	if phase == "crash" {
		_, billing := newBillingReliabilitySession(t, os.Getenv("BILLING_LEDGER_MODE"), os.Getenv("RENEWAPI_RESTART_SOURCE"))
		require.NoError(t, model.MigrateConversationState())
		c, info := prepareManagedTest(t, 401, 401, `{"model":"test","input":"durable question"}`)
		GetConversationTurn(c).RequestID = "billing-reliability-request"
		_, err := info.Conversation.ObserveResponse([]byte(`{"id":"upstream-durable","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"durable answer"}]}]}`), false)
		require.NoError(t, err)
		if stage == "before_prepare" {
			require.NoError(t, model.DB.Model(&model.BillingLedger{}).Where("id = ?", billing.ledgerID).Update("updated_at", time.Now().Unix()-21601).Error)
			os.Exit(23)
		}
		require.NoError(t, info.Conversation.PrepareCommit(200, billing.ledgerID))
		require.NoError(t, model.DB.Model(&model.BillingLedger{}).Where("id = ?", billing.ledgerID).Update("next_retry_at", 0).Error)
		if stage == "prepared" {
			os.Exit(23)
		}
		require.NoError(t, billing.Settle(200))
		if stage == "settled" {
			os.Exit(23)
		}
		require.NoError(t, info.Conversation.PublishCommit())
		if stage == "published" {
			os.Exit(23)
		}
		require.Equal(t, "delivered", stage)
		CompleteConversationDelivery(c)
		os.Exit(23)
	}
	require.Equal(t, "recover", phase)
	for range 2 {
		_, err := ReconcileBillingOnce(ctx, 100)
		require.NoError(t, err)
		require.NoError(t, model.ReconcileConversationCommits(ctx, 100))
	}
	var ledger model.BillingLedger
	require.NoError(t, model.DB.Where("request_id = ?", "billing-reliability-request").First(&ledger).Error)
	expected := 200
	var receipts []model.ConversationTurnCommit
	require.NoError(t, model.DB.Find(&receipts).Error)
	if stage == "before_prepare" {
		expected = 0
		require.Empty(t, receipts)
		require.Equal(t, model.BillingLedgerStateRefunded, ledger.State)
	} else {
		require.Len(t, receipts, 1)
		require.Equal(t, model.BillingLedgerStateSettled, ledger.State)
		checkpoint, err := model.FindConversationCheckpoint(ctx, receipts[0].Owner, receipts[0].CheckpointID)
		require.NoError(t, err)
		turn := &ConversationTurn{Owner: checkpoint.Owner, Settings: operation_setting.GetConversationRecoverySetting()}
		items, err := turn.replay(ctx, checkpoint)
		require.NoError(t, err)
		require.Len(t, items, 2)
		require.Equal(t, "durable answer", gjson.GetBytes(items[1], "content.0.text").String())
		expectedState := "published"
		if stage == "delivered" {
			expectedState = "delivered"
		}
		require.Equal(t, expectedState, receipts[0].State)
	}
	if ledger.FundingSource == BillingSourceSubscription {
		var subscription model.UserSubscription
		require.NoError(t, model.DB.First(&subscription, ledger.SubscriptionID).Error)
		require.EqualValues(t, expected, subscription.AmountUsed)
		require.Equal(t, 1000, getUserQuota(t, 401))
	} else {
		require.Equal(t, 1000-expected, getUserQuota(t, 401))
	}
	require.Equal(t, 1000-expected, getTokenRemainQuota(t, 401))
	require.Equal(t, expected, getTokenUsedQuota(t, 401))
}
