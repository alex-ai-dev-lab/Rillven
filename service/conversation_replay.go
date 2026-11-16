package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const conversationContextKey = "renewapi_prepared_conversation"
const ManagedResponsePrefix = "resp_renew_"
const conversationTurnLifetime = 600 * time.Second

type conversationPayload struct {
	Version      int                 `json:"version"`
	Input        []common.RawMessage `json:"input"`
	Output       []common.RawMessage `json:"output"`
	Compact      bool                `json:"compact,omitempty"`
	Materialized bool                `json:"materialized,omitempty"`
	Response     common.RawMessage   `json:"response,omitempty"`
}

// A request owns its immutable input and parent graph. Each attempt has a fresh
// output collector, and only the winning attempt may prepare a durable receipt.
type ConversationTurn struct {
	Owner, SessionID, ResponseID, RequestID         string
	Parent                                          *model.ConversationCheckpoint
	Input, Delta                                    []common.RawMessage
	Original                                        []byte
	Settings                                        operation_setting.ConversationRecoverySetting
	Compact, Portable, UnsafeTools, Managed, Shadow bool
	UnknownState                                    bool
	LookupError                                     error
	Attempts                                        int
	ReplayInput                                     []common.RawMessage
	Attempt                                         *conversationAttempt
}

func conversationHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func conversationCheckpointAAD(node *model.ConversationCheckpoint) string {
	// Bind ancestry and upstream ownership as well as plaintext. Mutable delivery
	// state and byte accounting are deliberately outside the authenticated data.
	metadata, _ := common.Marshal([]any{node.Owner, node.ID, node.SessionID, node.ParentID, node.ChannelID, node.SlotID, node.RouteKey, node.UpstreamID, node.Depth, node.Portable, node.Shadow, node.NativeState})
	if node.AccountID != "" {
		metadata, _ = common.Marshal([]any{node.AccountID, conversationHash(metadata)})
		return "conversation/v2/" + conversationHash(metadata)
	}
	return "conversation/v1/" + conversationHash(metadata)
}

func ConversationOwner(c *gin.Context) string {
	return conversationHash([]byte(fmt.Sprintf("v1/%d/%d/%s", common.GetContextKeyInt(c, constant.ContextKeyUserId), common.GetContextKeyInt(c, constant.ContextKeyTokenId), conversationHash([]byte(common.GetContextKeyString(c, constant.ContextKeyTokenKey))))))
}

func GetConversationTurn(c *gin.Context) *ConversationTurn {
	value, _ := c.Get(conversationContextKey)
	turn, _ := value.(*ConversationTurn)
	return turn
}

func ConversationPreferredChannel(c *gin.Context) int {
	if turn := GetConversationTurn(c); turn != nil && turn.Parent != nil {
		return turn.Parent.ChannelID
	}
	return 0
}

func conversationInput(raw common.RawMessage) ([]common.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if gjson.ParseBytes(raw).Type == gjson.String {
		item, err := common.Marshal(map[string]string{"role": "user", "content": gjson.ParseBytes(raw).String()})
		return []common.RawMessage{item}, err
	}
	var items []common.RawMessage
	if string(raw) == "null" || common.Unmarshal(raw, &items) != nil {
		return nil, errors.New("invalid Responses input")
	}
	return items, nil
}

func ReplaceConversationBody(c *gin.Context, body []byte) error {
	storage, err := common.CreateBodyStorage(body)
	if err != nil {
		return err
	}
	common.CleanupBodyStorage(c)
	c.Set(common.KeyBodyStorage, storage)
	c.Set(common.KeyRequestBody, nil)
	c.Request.Body = io.NopCloser(storage)
	c.Request.ContentLength = int64(len(body))
	return nil
}

// Runs after authentication and before distribution. Known gateway IDs must be
// readable even when admission is disabled during a rollback/drain.
func PrepareConversationContext(c *gin.Context) *types.NewAPIError {
	if c.Request.Method != "POST" || !(strings.HasSuffix(c.Request.URL.Path, "/responses") || strings.HasSuffix(c.Request.URL.Path, "/responses/compact")) {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}
	body, err := storage.Bytes()
	if err != nil {
		return types.NewConversationStoreUnavailable()
	}
	var fields map[string]common.RawMessage
	if common.Unmarshal(body, &fields) != nil || fields == nil {
		return types.NewError(errors.New("invalid Responses request"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	input, err := conversationInput(fields["input"])
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	t := &ConversationTurn{Owner: ConversationOwner(c), SessionID: uuid.NewString(), ResponseID: ManagedResponsePrefix + strings.ReplaceAll(uuid.NewString(), "-", ""), Original: append([]byte(nil), body...), Input: input, Delta: input, Settings: operation_setting.GetConversationRecoverySetting(), Portable: true, Compact: strings.HasSuffix(c.Request.URL.Path, "/compact")}
	c.Set(conversationContextKey, t)
	previous := gjson.GetBytes(body, "previous_response_id").String()
	var parent *model.ConversationCheckpoint
	if previous != "" {
		if strings.HasPrefix(previous, ManagedResponsePrefix) {
			parent, err = model.FindConversationCheckpoint(c.Request.Context(), t.Owner, previous)
		} else if _, cipherErr := common.RelayStateCipher(); cipherErr == nil {
			parent, err = model.FindConversationAlias(c.Request.Context(), t.Owner, conversationHash([]byte(previous)))
			if errors.Is(err, model.ErrConversationMissing) {
				err = nil
			}
			if err == nil && parent != nil && parent.Shadow {
				channel, channelErr := model.CacheGetChannel(parent.ChannelID)
				if channelErr != nil {
					err = channelErr
				}
				if channel == nil || channel.GetSetting().ConversationRecovery != "managed" {
					parent = nil
				}
			}
		}
		if err != nil {
			if strings.HasPrefix(previous, ManagedResponsePrefix) {
				return conversationAPIError(err)
			}
			t.LookupError, parent = err, nil
		}
		if parent == nil {
			t.UnknownState = true
		}
	}
	// A compaction capsule replaces a prefix. Only its complete, owner-scoped
	// preimage can replace it; a capsule next to an explicit parent is ambiguous.
	for index, item := range input {
		if gjson.GetBytes(item, "type").String() != "compaction" {
			continue
		}
		if index != 0 || previous != "" {
			t.UnknownState = true
			continue
		}
		if _, cipherErr := common.RelayStateCipher(); cipherErr != nil {
			t.UnknownState = true
			continue
		}
		parent, err = model.FindConversationResource(c.Request.Context(), t.Owner, "compaction", conversationHash([]byte(gjson.GetBytes(item, "encrypted_content").String())))
		if errors.Is(err, model.ErrConversationMissing) {
			parent = nil
			t.UnknownState = true
			continue
		}
		if err != nil {
			t.LookupError, parent = err, nil
			continue
		}
		if parent.Shadow {
			channel, channelErr := model.CacheGetChannel(parent.ChannelID)
			if channelErr != nil {
				t.LookupError, parent = channelErr, nil
				continue
			}
			if channel == nil || channel.GetSetting().ConversationRecovery != "managed" {
				parent, t.UnknownState = nil, true
				continue
			}
		}
		t.Delta = input[1:]
	}
	if parent != nil {
		t.Parent, t.SessionID, t.Managed = parent, parent.SessionID, true
		if err := model.AdmitConversationSession(c.Request.Context(), t.Owner, t.SessionID, int64(t.Settings.IdleTTLSeconds), int64(conversationTurnLifetime/time.Second)); err != nil {
			return conversationAPIError(err)
		}
		history, err := t.replay(c.Request.Context(), parent)
		if err != nil {
			return conversationAPIError(err)
		}
		t.Input = append(history, t.Delta...)
		if parent.AccountID != "" && previous != "" {
			t.Input, t.Delta, err = resolveAccountContinuation(history, input, c.GetHeader("X-Rillven-History-Mode"))
			if err != nil {
				return conversationAPIError(err)
			}
		}
		t.Portable = parent.Portable
		fields["input"], _ = common.Marshal(t.Input)
		delete(fields, "previous_response_id")
		if rewritten, marshalErr := common.Marshal(fields); marshalErr != nil {
			return types.NewConversationStoreUnavailable()
		} else if len(rewritten) > t.Settings.MaxReplayBytes {
			return conversationAPIError(model.ErrConversationLimit)
		} else if err := ReplaceConversationBody(c, rewritten); err != nil {
			return types.NewConversationStoreUnavailable()
		}
	}
	if len(body) > t.Settings.MaxReplayBytes {
		t.UnknownState = true
	}
	t.Portable = t.Portable && portableConversationItems(t.Input)
	t.ReplayInput = append([]common.RawMessage(nil), t.Input...)
	t.UnsafeTools = hasUnrecoverableServerTools(fields["tools"])
	if raw := fields["conversation"]; len(raw) > 0 && string(raw) != "null" {
		t.UnknownState = true
	}
	if t.Managed {
		c.Set(ContextConversationSession, t.SessionID)
	}
	return nil
}

func (t *ConversationTurn) replay(ctx context.Context, parent *model.ConversationCheckpoint) ([]common.RawMessage, error) {
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return nil, err
	}
	nodes := make([]conversationPayload, 0, parent.Depth)
	seen := make(map[string]bool)
	bytes := 0
	for node := parent; node != nil; {
		if seen[node.ID] || len(nodes) >= t.Settings.MaxDepth {
			return nil, model.ErrConversationLimit
		}
		seen[node.ID] = true
		if node.Bytes > 2*int64(t.Settings.MaxReplayBytes) {
			return nil, model.ErrConversationLimit
		}
		plain, err := cipher.Open(conversationCheckpointAAD(node), string(node.Ciphertext))
		if err != nil {
			return nil, err
		}
		bytes += len(plain)
		if bytes > t.Settings.MaxReplayBytes || conversationHash(plain) != node.ContentHash {
			return nil, model.ErrConversationLimit
		}
		var payload conversationPayload
		if common.Unmarshal(plain, &payload) != nil || payload.Version != 1 {
			return nil, errors.New("unsupported conversation checkpoint")
		}
		nodes = append(nodes, payload)
		if node.ParentID == "" || payload.Materialized {
			break
		}
		node, err = model.ReadConversationParent(ctx, t.Owner, t.SessionID, node.ParentID)
		if err != nil {
			return nil, err
		}
	}
	var items []common.RawMessage
	for i := len(nodes) - 1; i >= 0; i-- {
		items = append(items, nodes[i].Input...)
		if !nodes[i].Compact {
			items = append(items, nodes[i].Output...)
		}
	}
	return items, nil
}

// Account-private resources have no portable upload contract in this gateway.
// Only explicit protocol reference fields are inspected, never tool text.
func portableConversationItems(items []common.RawMessage) bool {
	for _, raw := range items {
		item := gjson.ParseBytes(raw)
		if len(accountPrivateReferences(item)) > 0 || item.Get("encrypted_content").Exists() || item.Get("reference_id").Exists() || item.Get("resource_id").Exists() {
			return false
		}
		switch item.Get("type").String() {
		case "", "message", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
		default:
			return false
		}
		for _, content := range item.Get("content").Array() {
			if content.Get("file_id").Exists() || content.Get("container_id").Exists() || content.Get("image_url").String() != "" && !strings.HasPrefix(content.Get("image_url").String(), "data:") {
				return false
			}
			for _, annotation := range content.Get("annotations").Array() {
				if annotation.Get("file_id").Exists() || annotation.Get("container_id").Exists() {
					return false
				}
			}
		}
	}
	return true
}

func hasUnrecoverableServerTools(raw common.RawMessage) bool {
	for _, tool := range gjson.ParseBytes(raw).Array() {
		switch tool.Get("type").String() {
		case "", "function", "custom", "web_search", "web_search_preview":
		case "namespace":
			if hasUnrecoverableServerTools([]byte(tool.Get("tools").Raw)) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func validateConversationToolPairs(items []common.RawMessage) error {
	calls, outputs := make(map[string]string), make(map[string]bool)
	for _, raw := range items {
		item := gjson.ParseBytes(raw)
		typ, id := item.Get("type").String(), item.Get("call_id").String()
		switch typ {
		case "function_call", "custom_tool_call":
			if id == "" || calls[id] != "" {
				return model.ErrConversationMissing
			}
			calls[id] = typ
		case "function_call_output", "custom_tool_call_output":
			expected := strings.TrimSuffix(typ, "_output")
			if id == "" || calls[id] != expected || outputs[id] {
				return model.ErrConversationMissing
			}
			outputs[id] = true
		}
	}
	return nil
}

func conversationAPIError(err error) *types.NewAPIError {
	if errors.Is(err, model.ErrConversationMissing) || errors.Is(err, model.ErrConversationBusy) {
		return types.NewConversationContextUnavailable()
	}
	if errors.Is(err, model.ErrConversationLimit) {
		return types.NewErrorWithStatusCode(errors.New("Conversation context exceeds the configured recovery limit."), types.ErrorCodeConversationContextUnavailable, 413, types.ErrOptionWithSkipRetry())
	}
	return types.NewConversationStoreUnavailable()
}

func ActivateConversation(c *gin.Context, info *relaycommon.RelayInfo) (apiErr *types.NewAPIError) {
	t := GetConversationTurn(c)
	if t == nil {
		return nil
	}
	settings, _ := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting)
	if info.ChannelMeta != nil {
		settings = info.ChannelSetting
	}
	mode := settings.ConversationRecovery
	if mode != "managed" && mode != "shadow" && !t.Managed {
		return nil
	}
	t.Shadow = mode == "shadow" && !t.Managed
	t.Managed = !t.Shadow
	defer func() {
		if t.Shadow && apiErr != nil {
			t.Shadow, info.Conversation, apiErr = false, nil, nil
		}
	}()
	if t.LookupError != nil {
		return conversationAPIError(t.LookupError)
	}
	if _, err := common.RelayStateCipher(); err != nil {
		return types.NewConversationStoreUnavailable()
	}
	if t.UnknownState {
		if t.Shadow {
			t.Shadow = false
			return nil
		}
		return types.NewConversationContextUnavailable()
	}
	if len(t.Original) > t.Settings.MaxReplayBytes || t.Parent != nil && t.Parent.Depth >= t.Settings.MaxDepth {
		return conversationAPIError(model.ErrConversationLimit)
	}
	if err := validateConversationToolPairs(t.Input); err != nil {
		return conversationAPIError(err)
	}
	t.RequestID = info.RequestId
	if failure := claimOrReplayConversationRequest(c, t, info); failure != nil {
		return failure
	}
	if t.Managed {
		c.Set(ContextConversationSession, t.SessionID)
	}
	return nil
}
