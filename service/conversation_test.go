package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func prepareManagedTest(t *testing.T, owner, channel int, body string) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyUserId, owner)
	common.SetContextKey(c, constant.ContextKeyTokenId, owner)
	common.SetContextKey(c, constant.ContextKeyTokenKey, fmt.Sprint(owner))
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	require.Nil(t, PrepareConversationContext(c))
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalBodyReusable(c, &request))
	info := &relaycommon.RelayInfo{RequestId: uuid.NewString(), Request: &request, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel, ChannelType: 1, ApiKey: fmt.Sprint(channel), ChannelSetting: dto.ChannelSettings{ConversationRecovery: "managed"}}}
	require.Nil(t, ActivateConversation(c, info))
	require.Nil(t, BeginConversationAttempt(c, info))
	return c, info
}

func publishManagedTest(t *testing.T, info *relaycommon.RelayInfo, output string) string {
	t.Helper()
	response := fmt.Sprintf(`{"id":"resp_%s","status":"completed","output":%s}`, uuid.NewString(), output)
	mapped, err := info.Conversation.ObserveResponse([]byte(response), false)
	require.NoError(t, err)
	require.NoError(t, info.Conversation.PrepareCommit(0, 0))
	require.NoError(t, info.Conversation.PublishCommit())
	return gjson.GetBytes(mapped, "id").String()
}

func TestManagedConversationGraphIsolationBranchesAndLease(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateConversationState())
	c, first := prepareManagedTest(t, 1, 10, `{"model":"test","input":"same text"}`)
	id := publishManagedTest(t, first, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"}]}]`)
	_, second := prepareManagedTest(t, 1, 20, fmt.Sprintf(`{"model":"test","previous_response_id":%q,"input":"same text","instructions":"current only"}`, id))
	replayed := second.Request.(*dto.OpenAIResponsesRequest)
	require.Empty(t, replayed.PreviousResponseID)
	require.Equal(t, int64(3), gjson.GetBytes(replayed.Input, "#").Int())
	require.Equal(t, "same text", gjson.GetBytes(replayed.Input, "2.content").String())
	idB := publishManagedTest(t, second, `[{"type":"function_call","name":"lookup","call_id":"call_1","arguments":"{}"}]`)
	_, branch := prepareManagedTest(t, 1, 30, fmt.Sprintf(`{"model":"test","previous_response_id":%q,"input":"branch"}`, id))
	publishManagedTest(t, branch, `[]`)
	thirdCtx, third := prepareManagedTest(t, 1, 20, fmt.Sprintf(`{"model":"test","previous_response_id":%q,"input":[{"type":"function_call_output","call_id":"call_1","output":"done"}]}`, idB))
	require.Equal(t, 20, ConversationPreferredChannel(thirdCtx))
	require.Equal(t, int64(5), gjson.GetBytes(third.Request.(*dto.OpenAIResponsesRequest).Input, "#").Int())
	_, err := model.FindConversationCheckpoint(context.Background(), "another-owner", id)
	require.ErrorIs(t, err, model.ErrConversationMissing)
	turn := GetConversationTurn(c)
	require.NoError(t, model.DB.Model(&model.ConversationSession{}).Where("id = ?", turn.SessionID).Update("expires_at", time.Now().Unix()-1).Error)
	require.NoError(t, model.CleanupConversationState(context.Background(), 10))
	var count int64
	require.NoError(t, model.DB.Model(&model.ConversationCheckpoint{}).Count(&count).Error)
	require.EqualValues(t, 3, count, "in-flight parent graph must survive cleanup")
	_, err = model.FindConversationCheckpoint(context.Background(), turn.Owner, id)
	require.ErrorIs(t, err, model.ErrConversationMissing, "new admission cannot revive expired history")
	require.NoError(t, model.DB.Model(&model.ConversationSession{}).Where("id = ?", turn.SessionID).Update("active_until", time.Now().Unix()-1).Error)
	require.NoError(t, model.CleanupConversationState(context.Background(), 10))
	require.NoError(t, model.DB.Model(&model.ConversationCheckpoint{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestManagedConversationCompactionPreimageAndIDMapping(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateConversationState())
	c, info := prepareManagedTest(t, 1, 10, `{"model":"test","input":"complete preimage"}`)
	GetConversationTurn(c).Compact = true
	id := publishManagedTest(t, info, `[{"type":"compaction","encrypted_content":"opaque-A"}]`)
	_, next := prepareManagedTest(t, 1, 20, `{"model":"test","input":[{"type":"compaction","encrypted_content":"opaque-A"},{"role":"user","content":"continue"}]}`)
	input := next.Request.(*dto.OpenAIResponsesRequest).Input
	require.NotContains(t, string(input), "opaque-A")
	require.Equal(t, int64(2), gjson.GetBytes(input, "#").Int())
	require.Contains(t, string(input), "complete preimage")
	response, err := next.Conversation.ObserveResponse([]byte(`{"type":"response.created","response":{"id":"resp_B","previous_response_id":"resp_A"}}`), true)
	require.NoError(t, err)
	require.Equal(t, id, gjson.GetBytes(response, "response.previous_response_id").String())
	response, err = next.Conversation.ObserveResponse([]byte(`{"type":"response.output_text.delta","response_id":"resp_B","delta":"resp_B"}`), true)
	require.NoError(t, err)
	require.Equal(t, "resp_B", gjson.GetBytes(response, "delta").String(), "text must not be rewritten")
	require.NotEqual(t, "resp_B", gjson.GetBytes(response, "response_id").String())
}

func TestManagedConversationRejectsOrphanToolOutput(t *testing.T) {
	for _, input := range []string{
		`[{"type":"function_call_output","call_id":"orphan","output":"done"}]`,
		`[{"type":"function_call","call_id":"same"},{"type":"function_call","call_id":"same"}]`,
		`[{"type":"custom_tool_call","call_id":"mixed"},{"type":"function_call_output","call_id":"mixed"}]`,
	} {
		var items []json.RawMessage
		require.NoError(t, common.Unmarshal([]byte(input), &items))
		require.Error(t, validateConversationToolPairs(items))
	}
}

func TestManagedConversationNativeIncrementalAndRetrySnapshot(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateConversationState())
	_, first := prepareManagedTest(t, 1, 10, `{"model":"test","input":"first","store":true}`)
	mapped, err := first.Conversation.ObserveResponse([]byte(`{"id":"resp_native","store":true,"status":"completed","output":[{"role":"assistant","type":"message","content":[{"type":"output_text","text":"answer"}]}]}`), false)
	require.NoError(t, err)
	require.NoError(t, first.Conversation.PrepareCommit(0, 0))
	require.NoError(t, first.Conversation.PublishCommit())
	c, second := prepareManagedTest(t, 1, 10, fmt.Sprintf(`{"model":"test","input":"next","previous_response_id":%q}`, gjson.GetBytes(mapped, "id").String()))
	require.Equal(t, "resp_native", second.Request.(*dto.OpenAIResponsesRequest).PreviousResponseID)
	require.EqualValues(t, 1, gjson.GetBytes(second.Request.(*dto.OpenAIResponsesRequest).Input, "#").Int())
	require.Nil(t, BeginConversationAttempt(c, second))
	require.Empty(t, second.Request.(*dto.OpenAIResponsesRequest).PreviousResponseID)
	require.EqualValues(t, 3, gjson.GetBytes(second.Request.(*dto.OpenAIResponsesRequest).Input, "#").Int())
}

func TestManagedConversationShadowBackendFailureIsPassive(t *testing.T) {
	setupIdentityStateTest(t)
	t.Setenv("RENEWAPI_STATE_KEYS", "")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":"hello"}`))
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	require.Nil(t, PrepareConversationContext(c))
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{ConversationRecovery: "shadow"}}}
	require.Nil(t, ActivateConversation(c, info))
	require.Nil(t, BeginConversationAttempt(c, info))
	require.Nil(t, info.Conversation)
	require.Empty(t, c.GetString(ContextConversationSession))
}

func TestManagedConversationRejectsMismatchedTerminal(t *testing.T) {
	setupIdentityStateTest(t)
	c, info := prepareManagedTest(t, 1, 10, `{"model":"test","input":"hi"}`)
	_, err := info.Conversation.ObserveResponse([]byte(`{"type":"response.output_text.delta","response_id":"resp_a","output_index":0,"content_index":0,"delta":"visible"}`), true)
	require.NoError(t, err)
	_, err = info.Conversation.ObserveResponse([]byte(`{"type":"response.completed","response":{"id":"resp_a","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"different"}]}]}}`), true)
	require.Error(t, err)
	require.False(t, GetConversationTurn(c).Attempt.complete)
}

func TestManagedConversationRejectsChangedToolIdentity(t *testing.T) {
	setupIdentityStateTest(t)
	for _, field := range []string{"type", "id", "call_id", "name", "namespace"} {
		t.Run(field, func(t *testing.T) {
			_, info := prepareManagedTest(t, 1, 10, `{"model":"test","input":"hello"}`)
			item := map[string]string{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "inspect", "namespace": "functions", "arguments": "{}"}
			added, err := common.Marshal(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
			require.NoError(t, err)
			_, err = info.Conversation.ObserveResponse(added, true)
			require.NoError(t, err)
			item[field] = "changed"
			terminal, err := common.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_a", "status": "completed", "output": []any{item}}})
			require.NoError(t, err)
			_, err = info.Conversation.ObserveResponse(terminal, true)
			require.ErrorContains(t, err, "identity")
			require.False(t, info.Conversation.CommitPrepared())
		})
	}
}

func TestManagedConversationRejectsTamperedOwnership(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateConversationState())
	c, info := prepareManagedTest(t, 1, 10, `{"model":"test","input":"private"}`)
	id := publishManagedTest(t, info, `[]`)
	turn := GetConversationTurn(c)
	node, err := model.FindConversationCheckpoint(context.Background(), turn.Owner, id)
	require.NoError(t, err)
	node.ChannelID++
	_, err = turn.replay(context.Background(), node)
	require.Error(t, err, "binding metadata is authenticated with the ciphertext")
}
