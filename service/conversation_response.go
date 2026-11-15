package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type conversationAttempt struct {
	turn                          *ConversationTurn
	info                          *relaycommon.RelayInfo
	ctx                           context.Context
	channelID                     int
	slotID, routeKey, upstreamID  string
	output                        []json.RawMessage
	resources                     []model.ConversationResource
	complete, prepared, published bool
	nativeState                   bool
	deltas                        map[string]hash.Hash
	deltaBytes                    int
	itemIdentities                map[int][6]string
	identityBytes                 int
}

var conversationItemIdentityFields = [...]string{"type", "id", "call_id", "name", "namespace", "role"}

func (a *conversationAttempt) observeItemIdentity(root gjson.Result) error {
	index := root.Get("output_index")
	if index.Type != gjson.Number || index.Num != float64(index.Int()) || index.Int() < 0 || index.Int() > int64(a.turn.Settings.MaxReplayBytes) {
		return errors.New("invalid response output index")
	}
	item := root.Get("item")
	if !item.IsObject() || item.Get("type").String() == "" {
		return errors.New("invalid response output item")
	}
	if a.itemIdentities == nil {
		a.itemIdentities = make(map[int][6]string)
	}
	identity, exists := a.itemIdentities[int(index.Int())]
	if !exists {
		a.identityBytes += 128 // Bound map/entry overhead as well as string contents.
	}
	for i, field := range conversationItemIdentityFields {
		value := item.Get(field).String()
		if identity[i] != "" && value != identity[i] {
			return errors.New("inconsistent response output identity")
		}
		if identity[i] == "" {
			a.identityBytes += len(value)
			identity[i] = strings.Clone(value)
		}
	}
	if a.identityBytes > a.turn.Settings.MaxReplayBytes {
		return types.NewResponseBufferUnavailable()
	}
	a.itemIdentities[int(index.Int())] = identity
	return nil
}

func conversationCredentialID(channelType int, key string) (string, error) {
	if channelType == constant.ChannelTypeCodex {
		credential, err := parseCodexOAuthKey(key)
		if err != nil {
			return "", err
		}
		key, err = codexCredentialSubject(credential)
		if err != nil {
			return "", err
		}
	}
	return conversationHash([]byte(fmt.Sprintf("%d/%s", channelType, key))), nil
}

// Stable credential selection survives key reordering. The ordinary selector
// still enforces current enable/disable state and channel/group authorization.
func ResolveConversationCredential(c *gin.Context, channel *model.Channel) (string, int, bool) {
	t := GetConversationTurn(c)
	if t == nil || t.Parent == nil || t.Parent.ChannelID != channel.Id || !channel.ChannelInfo.IsMultiKey {
		return "", 0, false
	}
	for index, key := range channel.GetKeys() {
		if status, found := channel.ChannelInfo.MultiKeyStatusList[index]; found && status != common.ChannelStatusEnabled {
			continue
		}
		id, err := conversationCredentialID(channel.Type, key)
		if err == nil && id == t.Parent.SlotID {
			return key, index, true
		}
	}
	return "", 0, false
}

func BeginConversationAttempt(c *gin.Context, info *relaycommon.RelayInfo) (apiErr *types.NewAPIError) {
	t := GetConversationTurn(c)
	if t == nil || !t.Managed && !t.Shadow {
		return nil
	}
	defer func() {
		if t.Shadow && apiErr != nil {
			info.Conversation, apiErr = nil, nil
		}
	}()
	if common.GetContextKeyInt(c, constant.ContextKeyChannelId) > 0 {
		info.InitChannelMeta(c)
	}
	if t.Attempt != nil && t.Attempt.prepared {
		return types.NewConversationStoreUnavailable()
	}
	if t.Attempts >= t.Settings.MaxAttempts {
		if t.Shadow {
			info.Conversation = nil
			return nil
		}
		return types.NewConversationContextUnavailable()
	}
	t.Attempts++
	if info.ChannelConfigVersion > 0 {
		var count int64
		if err := model.DB.WithContext(c.Request.Context()).Model(&model.Channel{}).Where("id = ? AND config_version = ? AND status = ?", info.ChannelId, info.ChannelConfigVersion, common.ChannelStatusEnabled).Count(&count).Error; err != nil {
			return types.NewConversationStoreUnavailable()
		}
		if count != 1 {
			return types.NewConversationContextUnavailable()
		}
	}
	slot, err := conversationCredentialID(info.ChannelType, info.ApiKey)
	if err != nil {
		return types.NewConversationContextUnavailable()
	}
	// Include changes that invalidate upstream ownership, without persisting any
	// secret URL/headers. Device resets likewise invalidate a stateful binding.
	revision := int64(0)
	if mode := channelIdentityScope(info); mode == "per_channel" || mode == "shadow" {
		identity, identityErr := model.EnsureChannelIdentity(c.Request.Context(), info.ChannelId)
		if identityErr != nil {
			return types.NewConversationStoreUnavailable()
		}
		revision = identity.Revision
	}
	routeFields, err := common.Marshal([]any{info.ChannelType, info.ChannelBaseUrl, info.ChannelSetting.Proxy, info.ChannelSetting.EgressPolicy, info.ChannelSetting.ClientProfile, info.ChannelSetting.ClientVersion, revision, info.ChannelConfigVersion})
	if err != nil {
		return types.NewConversationStoreUnavailable()
	}
	route := conversationHash(routeFields)
	if t.Parent != nil {
		sameTarget := t.Parent.ChannelID == info.ChannelId && t.Parent.SlotID == slot && t.Parent.RouteKey == route
		if !t.Portable && !sameTarget {
			return types.NewConversationContextUnavailable()
		}
		if req, ok := info.Request.(*dto.OpenAIResponsesRequest); ok {
			copy := *req
			copy.Input, err = common.Marshal(t.Input)
			copy.PreviousResponseID = ""
			// Only a confirmed stored native response can use incremental input.
			// Every later attempt starts from the immutable full replay snapshot.
			if sameTarget && t.Parent.NativeState && t.Parent.UpstreamID != "" && t.Attempts == 1 {
				copy.Input, err = common.Marshal(t.Delta)
				copy.PreviousResponseID = t.Parent.UpstreamID
			}
			if err != nil {
				return types.NewConversationStoreUnavailable()
			}
			info.Request = &copy
			body, err := common.Marshal(copy)
			if err != nil {
				return types.NewConversationStoreUnavailable()
			}
			if err := ReplaceConversationBody(c, body); err != nil {
				return types.NewConversationStoreUnavailable()
			}
		} else {
			return types.NewConversationContextUnavailable()
		}
	}
	a := &conversationAttempt{turn: t, info: info, ctx: c.Request.Context(), channelID: info.ChannelId, slotID: slot, routeKey: route}
	t.Attempt, info.Conversation, info.SettlementError = a, a, nil
	if t.Shadow {
		info.Conversation = &shadowConversation{conversationAttempt: a}
	}
	return nil
}

func ConversationCanRetry(c *gin.Context, failure *types.NewAPIError) bool {
	t := GetConversationTurn(c)
	if t == nil || !t.Managed {
		return true
	}
	if t.Attempt != nil && t.Attempt.complete && t.Attempt.ValidateResponse() == nil {
		return false
	}
	if !t.Portable || t.Attempts >= t.Settings.MaxAttempts || t.Attempt != nil && t.Attempt.prepared {
		return false
	}
	// An explicit pre-execution 4xx rejection can move; a lost server-tool result
	// after acceptance has no general exactly-once recovery contract.
	if t.UnsafeTools && (failure == nil || failure.StatusCode < 400 || failure.StatusCode >= 500) {
		return false
	}
	if t.UnsafeTools && t.Attempt != nil && t.Attempt.upstreamID != "" {
		return false
	}
	return true
}

func (a *conversationAttempt) ObserveResponse(payload []byte, stream bool) ([]byte, error) {
	if string(payload) == "[DONE]" {
		return payload, nil
	}
	if a.complete {
		return nil, errors.New("response event after terminal")
	}
	if !gjson.ValidBytes(payload) {
		return nil, errors.New("invalid managed response")
	}
	root := gjson.ParseBytes(payload)
	if stream && (root.Get("type").String() == "response.output_item.added" || root.Get("type").String() == "response.output_item.done") {
		if err := a.observeItemIdentity(root); err != nil {
			return nil, err
		}
	}
	if stream && root.Get("delta").Type == gjson.String {
		field := ""
		switch root.Get("type").String() {
		case "response.output_text.delta":
			field = fmt.Sprintf("output.%d.content.%d.text", root.Get("output_index").Int(), root.Get("content_index").Int())
		case "response.function_call_arguments.delta":
			field = fmt.Sprintf("output.%d.arguments", root.Get("output_index").Int())
		case "response.custom_tool_call_input.delta":
			field = fmt.Sprintf("output.%d.input", root.Get("output_index").Int())
		}
		if field != "" {
			delta := root.Get("delta").String()
			a.deltaBytes += len(delta)
			if a.deltaBytes > a.turn.Settings.MaxReplayBytes {
				return nil, types.NewResponseBufferUnavailable()
			}
			if a.deltas == nil {
				a.deltas = make(map[string]hash.Hash)
			}
			if a.deltas[field] == nil {
				a.deltas[field] = sha256.New()
			}
			_, _ = a.deltas[field].Write([]byte(delta))
		}
	}
	response, path := root, ""
	if stream {
		response, path = root.Get("response"), "response."
		if id := root.Get("response_id"); id.Type == gjson.String && id.String() != "" {
			if len(id.String()) > 256 || a.upstreamID != "" && id.String() != a.upstreamID {
				return nil, errors.New("inconsistent response identity")
			}
			a.upstreamID = id.String()
			if !a.turn.Shadow {
				payload, _ = sjson.SetBytes(payload, "response_id", a.turn.ResponseID)
			}
		}
	}
	if id := response.Get("id").String(); id != "" {
		if len(id) > 256 || a.upstreamID != "" && a.upstreamID != id {
			return nil, errors.New("inconsistent response identity")
		}
		a.upstreamID = id
		if !a.turn.Shadow {
			payload, _ = sjson.SetBytes(payload, path+"id", a.turn.ResponseID)
		}
	}
	if response.Get("previous_response_id").Exists() && !a.turn.Shadow {
		var parent any
		if a.turn.Parent != nil {
			parent = a.turn.Parent.ID
		}
		payload, _ = sjson.SetBytes(payload, path+"previous_response_id", parent)
	}
	terminal := !stream || root.Get("type").String() == "response.completed" || root.Get("type").String() == "response.incomplete"
	if terminal {
		if a.complete {
			return nil, errors.New("duplicate response terminal")
		}
		if a.upstreamID == "" || !response.Get("output").IsArray() || response.Get("error").IsObject() {
			return nil, errors.New("incomplete managed response")
		}
		status := response.Get("status").String()
		a.nativeState = response.Get("store").Bool()
		output := response.Get("output").Array()
		for index, identity := range a.itemIdentities {
			if index >= len(output) {
				return nil, errors.New("response terminal is missing an emitted item")
			}
			for i, field := range conversationItemIdentityFields {
				if identity[i] != "" && output[index].Get(field).String() != identity[i] {
					return nil, errors.New("response terminal does not match emitted item identity")
				}
			}
		}
		for path, delta := range a.deltas {
			if conversationHash([]byte(response.Get(path).String())) != hex.EncodeToString(delta.Sum(nil)) {
				return nil, errors.New("response terminal does not match emitted content")
			}
		}
		if status != "" && status != "completed" && status != "incomplete" {
			return nil, errors.New("unsuccessful managed response")
		}
		if err := common.Unmarshal([]byte(response.Get("output").Raw), &a.output); err != nil {
			return nil, err
		}
		for _, raw := range a.output {
			item := gjson.ParseBytes(raw)
			if item.Get("type").String() == "compaction" {
				if item.Get("encrypted_content").String() == "" {
					return nil, errors.New("empty compaction capsule")
				}
				a.resources = append(a.resources, model.ConversationResource{Owner: a.turn.Owner, Kind: "compaction", Digest: conversationHash([]byte(item.Get("encrypted_content").String())), CheckpointID: a.turn.ResponseID, SessionID: a.turn.SessionID})
			}
		}
		a.complete = true
	}
	return payload, nil
}

func (a *conversationAttempt) ValidateResponse() error {
	if !a.complete {
		return errors.New("managed response has no complete terminal")
	}
	return validateConversationToolPairs(append(append([]json.RawMessage(nil), a.turn.Input...), a.output...))
}

func (a *conversationAttempt) PrepareCommit(quota int, ledgerID uint64) error {
	if err := a.ValidateResponse(); err != nil {
		return err
	}
	if a.prepared {
		return nil
	}
	if ledgerID == 0 && quota != 0 {
		return errors.New("managed conversation requires durable billing")
	}
	output := make([]json.RawMessage, 0, len(a.output))
	for _, item := range a.output {
		// Hidden reasoning has no portable decryption contract. Keep only the
		// visible message/tool history; never send another account's ciphertext.
		if kind := gjson.GetBytes(item, "type").String(); kind == "reasoning" || kind == "compaction" {
			continue
		}
		output = append(output, item)
	}
	t := a.turn
	payload := conversationPayload{Version: 1, Input: t.Delta, Output: output, Compact: t.Compact}
	if t.Parent != nil && (t.Parent.Depth+1)%32 == 0 {
		payload.Input, payload.Materialized = t.Input, true
	}
	plain, err := common.Marshal(payload)
	if err != nil {
		return err
	}
	if len(plain) > t.Settings.MaxReplayBytes {
		return model.ErrConversationLimit
	}
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	checkpoint := model.ConversationCheckpoint{ID: t.ResponseID, Owner: t.Owner, SessionID: t.SessionID, ChannelID: a.channelID, SlotID: a.slotID, RouteKey: a.routeKey, UpstreamID: a.upstreamID, UpstreamHash: conversationHash([]byte(a.upstreamID)), State: "prepared", ContentHash: conversationHash(plain), Depth: 1, Portable: t.Portable && (t.Compact || portableConversationItems(output)), CreatedAt: now}
	checkpoint.Shadow, checkpoint.NativeState = t.Shadow, a.nativeState
	if t.Parent != nil {
		checkpoint.ParentID, checkpoint.Depth = t.Parent.ID, t.Parent.Depth+1
	}
	encrypted, err := cipher.Seal(conversationCheckpointAAD(&checkpoint), plain)
	if err != nil {
		return err
	}
	checkpoint.Ciphertext, checkpoint.Bytes = model.StateBlob(encrypted), int64(len(encrypted))
	input := model.ConversationCommitInput{Session: model.ConversationSession{ID: t.SessionID, Owner: t.Owner, ExpiresAt: now + int64(t.Settings.IdleTTLSeconds), DeadlineAt: now + int64(t.Settings.MaxLifetimeSeconds), CreatedAt: now, ActiveUntil: now + int64(conversationTurnLifetime/time.Second)}, Checkpoint: checkpoint, Receipt: model.ConversationTurnCommit{RequestID: t.RequestID, Owner: t.Owner, SessionID: t.SessionID, CheckpointID: t.ResponseID, ChannelID: a.channelID, ActualQuota: int64(quota), BillingLedgerID: ledgerID, State: "prepared", CreatedAt: now}, Resources: a.resources, MaxOwnerBytes: int64(t.Settings.MaxOwnerBytes), MaxSessionBytes: int64(t.Settings.MaxSessionBytes)}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(a.ctx), 10*time.Second)
	defer cancel()
	if err := model.PrepareConversationCommit(ctx, input); err != nil {
		return err
	}
	a.prepared = true
	return nil
}

func (a *conversationAttempt) PublishCommit() error {
	if !a.prepared {
		return errors.New("conversation commit was not prepared")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(a.ctx), 10*time.Second)
	defer cancel()
	if err := model.PublishConversationCommit(ctx, a.turn.Owner, a.turn.RequestID); err != nil {
		return err
	}
	a.published = true
	return nil
}
func (a *conversationAttempt) CommitPrepared() bool  { return a.prepared }
func (a *conversationAttempt) ObservationOnly() bool { return false }
func (a *conversationAttempt) CommitPublished() bool { return a.published }
func (a *conversationAttempt) BufferLimits() (int, int) {
	return a.turn.Settings.MaxBufferBytes, a.turn.Settings.GlobalBufferBytes
}

func CompleteConversationDelivery(c *gin.Context) {
	if t := GetConversationTurn(c); t != nil && t.Attempt != nil && t.Attempt.published {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
		defer cancel()
		_ = model.MarkConversationDelivered(ctx, t.Owner, t.RequestID)
	}
}
