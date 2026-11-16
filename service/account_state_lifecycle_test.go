package service

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestAccountStateReencryptPreservesHistoryIdentityAndDeleteOwnership(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateConversationState())
	require.NoError(t, model.DB.AutoMigrate(&model.CodexRefreshJournal{}, &model.Channel{}))
	c, info := prepareManagedTest(t, 1, 10, `{"model":"test","input":"private test context"}`)
	id := publishManagedTest(t, info, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"saved"}]}]`)
	turn := GetConversationTurn(c)
	keys, err := common.Marshal(map[string]string{"test": base64.StdEncoding.EncodeToString(make([]byte, 32)), "next": base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))})
	require.NoError(t, err)
	t.Setenv("RENEWAPI_STATE_KEYS", string(keys))
	t.Setenv("RENEWAPI_STATE_ACTIVE_KEY", "next")
	updated, err := ReencryptRelayState(t.Context(), 64)
	require.NoError(t, err)
	require.Positive(t, updated)
	checkpoint, err := model.FindConversationCheckpoint(t.Context(), turn.Owner, id)
	require.NoError(t, err)
	require.Contains(t, string(checkpoint.Ciphertext), "v1.next.")
	payload, err := readCheckpointPayload(t.Context(), turn.Owner, checkpoint)
	require.NoError(t, err)
	require.NotEmpty(t, payload.Response)
	_, err = model.FindConversationCheckpoint(t.Context(), "another-owner", id)
	require.ErrorIs(t, err, model.ErrConversationMissing)
	require.ErrorIs(t, model.DeleteOwnedConversationSession(t.Context(), "another-owner", turn.SessionID), model.ErrConversationMissing)
	require.ErrorIs(t, model.DeleteOwnedConversationSession(t.Context(), turn.Owner, turn.SessionID), model.ErrConversationBusy)
	_, err = model.FindConversationCheckpoint(t.Context(), turn.Owner, id)
	require.ErrorIs(t, err, model.ErrConversationMissing, "deletion blocks access while cleanup waits")
	rows, err := model.ListConversationDeletions(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	// Simulate restoring a snapshot from before the deletion, then applying
	// the independent tombstone export before permitting traffic again.
	require.NoError(t, model.DB.Where("id = ?", rows[0].ID).Delete(&model.ConversationDeletion{}).Error)
	_, err = model.FindConversationCheckpoint(t.Context(), turn.Owner, id)
	require.NoError(t, err)
	require.NoError(t, model.ImportConversationDeletions(t.Context(), rows))
	_, err = model.FindConversationCheckpoint(t.Context(), turn.Owner, id)
	require.ErrorIs(t, err, model.ErrConversationMissing)
	require.NoError(t, model.DB.Model(&model.ConversationSession{}).Where("id = ?", turn.SessionID).Update("active_until", 0).Error)
	require.NoError(t, model.DeleteOwnedConversationSession(t.Context(), turn.Owner, turn.SessionID))
	_, err = model.FindConversationCheckpoint(t.Context(), turn.Owner, id)
	require.ErrorIs(t, err, model.ErrConversationMissing)
}

func TestAccountContinuationPrefixAndExplicitDelta(t *testing.T) {
	history := []common.RawMessage{[]byte(`{"role":"user","content":"first"}`), []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"saved"}]}`)}
	full := append(append([]common.RawMessage(nil), history...), []byte(`{"role":"user","content":"next"}`))
	for _, mode := range []string{"", "full"} {
		input, delta, err := resolveAccountContinuation(history, full, mode)
		require.NoError(t, err)
		require.Len(t, input, 3)
		require.Len(t, delta, 1)
	}
	edited := append([]common.RawMessage(nil), full...)
	edited[1] = []byte(strings.ReplaceAll(string(edited[1]), "saved", "changed"))
	for _, mode := range []string{"", "full"} {
		_, _, err := resolveAccountContinuation(history, edited, mode)
		require.ErrorIs(t, err, model.ErrConversationMissing)
	}
	_, _, err := resolveAccountContinuation(history, history[:1], "full")
	require.ErrorIs(t, err, model.ErrConversationMissing)
	input, delta, err := resolveAccountContinuation(history, history[:1], "delta")
	require.NoError(t, err)
	require.Len(t, input, 3, "explicit delta may repeat a user message")
	require.Len(t, delta, 1)
}

func TestAccountConfiguredContextBudgetRejectsBeforeAttempt(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateConversationState())
	c, info := prepareManagedTest(t, 1, 10, `{"model":"test","input":"prompt","max_output_tokens":100}`)
	turn := GetConversationTurn(c)
	info.ChannelSetting.UpstreamContextTokens = 1
	before := turn.Attempts
	require.NotNil(t, BeginConversationAttempt(c, info))
	require.Equal(t, before, turn.Attempts)
	info.ChannelSetting.UpstreamContextTokens = 100
	require.Nil(t, BeginConversationAttempt(c, info), "the explicit budget may equal the window")
	info.ChannelSetting.UpstreamContextTokens = 0
	require.Nil(t, BeginConversationAttempt(c, info), "zero defers window validation to the provider")
}

func TestAccountAffinityAndPrivateReferencesRemainOwnerBound(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateConversationState())
	c, info := prepareManagedTest(t, 1, 10, `{"model":"test","input":"prompt"}`)
	turn := GetConversationTurn(c)
	turn.Attempt.accountID = "account-a"
	ObserveConversationAffinityHeaders(info, map[string][]string{"X-Codex-Turn-State": {"token-a"}})
	id := publishManagedTest(t, info, `[{"type":"message","id":"msg_saved","role":"assistant","content":[{"type":"output_text","text":"saved"}]}]`)
	checkpoint, err := model.FindConversationCheckpoint(t.Context(), turn.Owner, id)
	require.NoError(t, err)
	supplied := &ConversationTurn{Owner: turn.Owner, Parent: checkpoint, Original: []byte(`{"client_metadata":{"x-codex-turn-state":"token-a"}}`)}
	require.NoError(t, validateAccountBodyAffinity(t.Context(), supplied, "account-a"))
	require.Error(t, validateAccountBodyAffinity(t.Context(), supplied, "account-b"))
	supplied.Owner = "another-owner"
	require.Error(t, validateAccountBodyAffinity(t.Context(), supplied, "account-a"))
	supplied.Owner, supplied.Original = turn.Owner, []byte(`{"client_metadata":{"parent_response_id":"guessed-response"}}`)
	require.Error(t, validateAccountBodyAffinity(t.Context(), supplied, "account-a"))
	supplied.Original = nil
	supplied.Input = []common.RawMessage{[]byte(`{"type":"message","role":"user","reference_id":"guessed-resource","content":"literal tool data is not scanned"}`)}
	_, err = projectAccountHistory(t.Context(), supplied, "account-a", false)
	require.Error(t, err)
}
