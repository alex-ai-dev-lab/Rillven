package service

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func readCheckpointPayload(ctx context.Context, owner string, checkpoint *model.ConversationCheckpoint) (conversationPayload, error) {
	var payload conversationPayload
	if checkpoint.Owner != owner {
		return payload, model.ErrConversationMissing
	}
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return payload, err
	}
	plain, err := cipher.Open(conversationCheckpointAAD(checkpoint), string(checkpoint.Ciphertext))
	if err != nil {
		return payload, err
	}
	if conversationHash(plain) != checkpoint.ContentHash || common.Unmarshal(plain, &payload) != nil || payload.Version != 1 {
		return payload, model.ErrConversationMissing
	}
	return payload, nil
}

func validateCompleteConversationToolPairs(items []common.RawMessage) error {
	if err := validateConversationToolPairs(items); err != nil {
		return err
	}
	pending := make(map[string]bool)
	for _, raw := range items {
		kind, id := gjson.GetBytes(raw, "type").String(), gjson.GetBytes(raw, "call_id").String()
		switch kind {
		case "function_call", "custom_tool_call":
			pending[id] = true
		case "function_call_output", "custom_tool_call_output":
			delete(pending, id)
		}
	}
	if len(pending) > 0 {
		return model.ErrConversationMissing
	}
	return nil
}

// Explicit references are resolved from owner-scoped encrypted snapshots, never
// by asking a shared upstream to retrieve a caller-supplied object ID.
func projectAccountHistory(ctx context.Context, turn *ConversationTurn, target string, migrating bool) ([]common.RawMessage, error) {
	items := append([]common.RawMessage(nil), turn.Input...)
	for index, raw := range items {
		item := gjson.ParseBytes(raw)
		kind, id := item.Get("type").String(), item.Get("id").String()
		if kind == "reasoning" && item.Get("encrypted_content").String() != "" && id == "" {
			checkpoint, err := model.FindConversationResource(ctx, turn.Owner, "reasoning", conversationHash([]byte(item.Get("encrypted_content").String())))
			if err != nil {
				return nil, err
			}
			if checkpoint.AccountID != target {
				return nil, model.ErrConversationMissing
			}
		}
		for _, reference := range accountPrivateReferences(item) {
			checkpoint, err := model.FindConversationResource(ctx, turn.Owner, reference.kind, conversationHash([]byte(reference.id)))
			if err != nil {
				return nil, err
			}
			if checkpoint.AccountID != target {
				return nil, model.ErrConversationMissing
			}
		}
		if id != "" && (kind == "item_reference" || item.Get("role").String() != "user") {
			checkpoint, err := model.FindConversationResource(ctx, turn.Owner, "item", conversationHash([]byte(id)))
			if err != nil {
				return nil, err
			}
			payload, err := readCheckpointPayload(ctx, turn.Owner, checkpoint)
			if err != nil {
				return nil, err
			}
			var stored common.RawMessage
			for _, candidate := range payload.Output {
				if gjson.GetBytes(candidate, "id").String() == id {
					stored = candidate
					break
				}
			}
			if stored == nil {
				return nil, model.ErrConversationMissing
			}
			if kind == "item_reference" {
				raw, items[index], item, kind = stored, stored, gjson.ParseBytes(stored), gjson.GetBytes(stored, "type").String()
			} else if field := accountItemContentMismatch(stored, raw); field != "" {
				return nil, fmt.Errorf("%w: item %s differs from its checkpoint", model.ErrConversationMissing, field)
			}
			if checkpoint.AccountID != target && !portableConversationItems([]common.RawMessage{raw}) {
				return nil, model.ErrConversationMissing
			}
		}
		if migrating {
			if !portableConversationItems([]common.RawMessage{raw}) {
				return nil, model.ErrConversationMissing
			}
			// IDs on materialized messages/tool calls are upstream bookkeeping.
			// Tool call_id and output pairing remain intact.
			if id != "" {
				var err error
				items[index], err = sjson.DeleteBytes(raw, "id")
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if migrating {
		if err := validateCompleteConversationToolPairs(items); err != nil {
			return nil, err
		}
	}
	if err := validateAccountBodyAffinity(ctx, turn, target); err != nil {
		return nil, err
	}
	return items, nil
}

func accountItemContentMismatch(stored, supplied []byte) string {
	for _, field := range []string{"type", "role", "content", "call_id", "name", "namespace", "arguments", "input", "encrypted_content", "summary"} {
		a, b := gjson.GetBytes(stored, field), gjson.GetBytes(supplied, field)
		if a.Exists() != b.Exists() {
			return field
		}
		if !a.Exists() {
			continue
		}
		// Adapters may represent function arguments as an object or a JSON
		// string. Compare their contents without rounding numeric literals.
		if field == "arguments" {
			if a.Type == gjson.String {
				a = gjson.Parse(a.String())
			}
			if b.Type == gjson.String {
				b = gjson.Parse(b.String())
			}
		}
		if !completeHistoryPrefix([]common.RawMessage{[]byte(a.Raw)}, []common.RawMessage{[]byte(b.Raw)}) {
			return field
		}
	}
	return ""
}

type accountPrivateReference struct{ kind, id string }

func accountPrivateReferences(item gjson.Result) []accountPrivateReference {
	var result []accountPrivateReference
	collect := func(value gjson.Result) {
		for _, field := range []string{"file_id", "container_id", "reference_id", "resource_id"} {
			if id := value.Get(field).String(); id != "" {
				kind := "file"
				if field == "container_id" {
					kind = "container"
				} else if field == "reference_id" || field == "resource_id" {
					kind = field
				}
				result = append(result, accountPrivateReference{kind: kind, id: id})
			}
		}
	}
	collect(item)
	for _, content := range item.Get("content").Array() {
		collect(content)
		for _, annotation := range content.Get("annotations").Array() {
			collect(annotation)
		}
	}
	return result
}

func validateAccountBodyAffinity(ctx context.Context, turn *ConversationTurn, target string) error {
	for _, path := range []string{"client_metadata", "metadata"} {
		for _, field := range []string{"x-codex-turn-state", "x-codex-routing-hint"} {
			value := gjson.GetBytes(turn.Original, path+"."+field)
			if !value.Exists() {
				continue
			}
			if value.Type != gjson.String || value.String() == "" {
				return model.ErrConversationMissing
			}
			checkpoint, err := model.FindConversationResource(ctx, turn.Owner, "affinity", conversationHash([]byte(value.String())))
			if err != nil {
				return err
			}
			if checkpoint.AccountID != target {
				return model.ErrConversationMissing
			}
		}
		if value := gjson.GetBytes(turn.Original, path+".parent_response_id"); value.Exists() {
			if value.Type != gjson.String || turn.Parent == nil || turn.Parent.AccountID != target {
				return model.ErrConversationMissing
			}
			if value.String() == turn.Parent.ID {
				continue
			}
			checkpoint, err := model.FindConversationAlias(ctx, turn.Owner, conversationHash([]byte(value.String())))
			if err != nil {
				return err
			}
			if checkpoint.ID != turn.Parent.ID {
				return model.ErrConversationMissing
			}
		}
	}
	return nil
}

// Responses with previous_response_id normally carry a delta. A complete
// prefix can be detected safely; a partial or edited historical prefix cannot
// be silently appended. Explicit delta mode permits intentional repetition.
func resolveAccountContinuation(history, input []common.RawMessage, mode string) ([]common.RawMessage, []common.RawMessage, error) {
	if mode != "" && mode != "full" && mode != "delta" {
		return nil, nil, model.ErrConversationMissing
	}
	if mode != "delta" && completeHistoryPrefix(history, input) {
		return input, input[len(history):], nil
	}
	if mode == "full" {
		return nil, nil, model.ErrConversationMissing
	}
	if mode == "" {
		if len(input) > 1 && len(history) > 0 && completeHistoryPrefix(history[:1], input[:1]) {
			return nil, nil, model.ErrConversationMissing
		}
		ids := make(map[string]bool)
		for _, raw := range history {
			if id := gjson.GetBytes(raw, "id").String(); id != "" {
				ids[id] = true
			}
		}
		for _, raw := range input {
			if ids[gjson.GetBytes(raw, "id").String()] {
				return nil, nil, model.ErrConversationMissing
			}
		}
	}
	return append(append([]common.RawMessage(nil), history...), input...), input, nil
}

func completeHistoryPrefix(history, input []common.RawMessage) bool {
	if len(history) == 0 || len(input) < len(history) {
		return false
	}
	for i, raw := range history {
		var a, b any
		if common.UnmarshalPreserveNumbers(raw, &a) != nil || common.UnmarshalPreserveNumbers(input[i], &b) != nil {
			return false
		}
		left, err := common.Marshal(a)
		if err != nil {
			return false
		}
		right, err := common.Marshal(b)
		if err != nil || string(left) != string(right) {
			return false
		}
	}
	return true
}
