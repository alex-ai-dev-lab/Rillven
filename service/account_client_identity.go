package service

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Reference fields share their target's namespace; unrelated ID kinds do not.
var accountIdentityFields = map[string]string{
	"installation_id": "installation", "x-codex-installation-id": "installation",
	"session_id": "session", "session-id": "session",
	"thread_id": "thread", "thread-id": "thread", "conversation_id": "thread",
	"parent_thread_id": "thread", "forked_from_thread_id": "thread", "parentThreadId": "thread", "x-codex-parent-thread-id": "thread",
	"window_id": "window", "x-codex-window-id": "window", "codexWindowId": "window", "context_window_id": "context-window",
	"first_window_id": "context-window", "previous_window_id": "context-window",
	"turn_id": "turn", "parent_turn_id": "turn", "root_turn_id": "turn", "turn-id": "turn", "x-client-request-id": "thread",
}

// The official client treats turn metadata as the source of compatibility
// headers and flat metadata. Reject contradictory aliases before projecting.
func collectAccountIdentity(headers http.Header, body []byte) (map[string]string, error) {
	var payload map[string]any
	if err := validateUniqueAccountMetadata(body, 0); err != nil {
		return nil, err
	}
	if common.UnmarshalPreserveNumbers(body, &payload) != nil || payload == nil {
		return nil, errors.New("invalid identity request")
	}
	result := make(map[string]string)
	canonical := map[string]string{
		"session-id": "session_id", "session_id": "session_id",
		"thread-id": "thread_id", "thread_id": "thread_id", "conversation_id": "thread_id", "x-codex-thread-id": "thread_id",
		"window_id": "window_id", "x-codex-window-id": "window_id", "codexWindowId": "window_id",
		"parent_thread_id": "parent_thread_id", "parentThreadId": "parent_thread_id", "x-codex-parent-thread-id": "parent_thread_id",
		"turn_id": "turn_id", "turn-id": "turn_id", "context_window_id": "context_window_id",
	}
	merge := func(values map[string]any) error {
		for name, key := range canonical {
			raw, ok := values[name]
			if !ok {
				continue
			}
			value, err := accountIdentityValue(accountIdentityFields[name], raw)
			if err != nil {
				return errors.New("invalid identity field type")
			}
			if value == "" {
				continue
			}
			if len(value) > 1024 {
				return errors.New("identity field exceeds limit")
			}
			if prior := result[key]; prior != "" && prior != value {
				return fmt.Errorf("conflicting %s aliases", key)
			}
			result[key] = value
		}
		return nil
	}
	containers := []map[string]any{payload}
	if raw, exists := payload["client_metadata"]; exists {
		value, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid client metadata")
		}
		containers = append(containers, value)
	}
	h := make(map[string]any)
	for name, values := range headers {
		key := strings.ToLower(name)
		if _, ok := canonical[key]; !ok && key != "x-codex-turn-metadata" {
			continue
		}
		for _, value := range values {
			if prior, ok := h[key]; ok && prior != value {
				return nil, errors.New("conflicting identity headers")
			}
			h[key] = value
		}
	}
	containers = append(containers, h)
	for _, values := range containers {
		if err := merge(values); err != nil {
			return nil, err
		}
		for _, key := range []string{"turnMetadata", "turn_metadata", "x-codex-turn-metadata"} {
			if raw, ok := values[key]; ok {
				metadata, err := decodeAccountTurnMetadata(raw)
				if err != nil {
					return nil, err
				}
				if err := merge(metadata); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}

func decodeAccountTurnMetadata(value any) (map[string]any, error) {
	var metadata map[string]any
	switch raw := value.(type) {
	case string:
		if len(raw) > 64<<10 || validateUniqueAccountMetadata([]byte(raw), 1) != nil || common.UnmarshalPreserveNumbers([]byte(raw), &metadata) != nil || metadata == nil {
			return nil, errors.New("invalid embedded turn metadata")
		}
	case map[string]any:
		metadata = raw
	default:
		return nil, errors.New("invalid embedded turn metadata")
	}
	return metadata, nil
}

func collectAccountTurnMetadata(headers http.Header, body []byte) (map[string]any, error) {
	metadata := make(map[string]any)
	if value := headers.Get("X-Codex-Turn-Metadata"); value != "" {
		var err error
		metadata, err = decodeAccountTurnMetadata(value)
		if err != nil {
			return nil, err
		}
	}
	var payload map[string]any
	if err := common.UnmarshalPreserveNumbers(body, &payload); err != nil {
		return nil, err
	}
	containers := []map[string]any{payload}
	if client, ok := payload["client_metadata"].(map[string]any); ok {
		containers = append(containers, client)
	}
	for _, container := range containers {
		for _, name := range []string{"turnMetadata", "turn_metadata", "x-codex-turn-metadata"} {
			if value, exists := container[name]; exists {
				full, err := decodeAccountTurnMetadata(value)
				if err != nil {
					return nil, err
				}
				for key, value := range full {
					metadata[key] = value
				}
			}
		}
	}
	return metadata, nil
}

func (p *AttemptClientProfile) resolveCodexCorrelations(input map[string]string, thread, requestID string) error {
	window := input["window_id"]
	if window == "" {
		window = thread + ":0"
	}
	var err error
	p.WindowID, err = p.accountField("window", window)
	if err != nil {
		return err
	}
	turn := input["turn_id"]
	if turn == "" {
		turn = "request:" + requestID
	}
	p.TurnID, err = p.correlationID("turn", turn)
	if err != nil {
		return err
	}
	contextWindow := input["context_window_id"]
	if contextWindow == "" {
		contextWindow = window
	}
	p.ContextWindowID, err = p.correlationID("context-window", contextWindow)
	if err != nil {
		return err
	}
	p.Headers.Set("X-Codex-Window-Id", p.WindowID)
	if parent := input["parent_thread_id"]; parent != "" {
		id, err := p.correlationID("thread", parent)
		if err != nil {
			return err
		}
		p.Headers.Set("X-Codex-Parent-Thread-Id", id)
	}
	return nil
}

func (p *AttemptClientProfile) correlationID(kind, raw string) (string, error) {
	if _, ok := p.mapped[kind+"/"+raw]; ok {
		return raw, nil
	}
	key := kind + "/" + raw
	if id := p.correlations[key]; id != "" {
		return id, nil
	}
	digest := hex.EncodeToString(identityProjection(p.seed, p.Provider, p.AccountID, p.owner, raw, fmt.Sprint(p.IdentityRevision), kind))
	owner := hex.EncodeToString(identityProjection(p.seed, "owner", p.owner))
	var result string
	var err error
	if kind == "context-window" && isModelFacingWindow(raw) {
		sequence := identityProjection(p.seed, p.AccountID, p.owner, p.ThreadID, fmt.Sprint(p.IdentityRevision), "context-sequence/v1")
		allocation, allocationErr := model.ResolveAccountContextAllocation(p.ctx, p.AccountID, owner, hex.EncodeToString(sequence), digest, binary.LittleEndian.Uint64(sequence[:8]))
		result, err = allocation.String(), allocationErr
	} else {
		result, err = model.ResolveAccountCorrelation(p.ctx, p.AccountID, owner, digest)
	}
	if err != nil {
		return "", err
	}
	if p.correlations == nil {
		p.correlations = make(map[string]string)
	}
	p.correlations[key] = result
	if p.mapped == nil {
		p.mapped = make(map[string]struct{})
	}
	p.mapped[kind+"/"+result] = struct{}{}
	return result, nil
}

func (p *AttemptClientProfile) accountField(kind, raw string) (string, error) {
	switch kind {
	case "installation":
		return p.InstallationID, nil
	case "session":
		return p.SessionID, nil
	case "window":
		if thread, number, ok := strings.Cut(raw, ":"); ok {
			if n, err := strconv.ParseUint(number, 10, 64); err == nil && strconv.FormatUint(n, 10) == number {
				id, err := p.correlationID("thread", thread)
				return id + ":" + number, err
			}
		}
	}
	return p.correlationID(kind, raw)
}

func rewriteAccountMetadata(values map[string]any, p *AttemptClientProfile) error {
	for name, kind := range accountIdentityFields {
		raw, exists := values[name]
		if !exists {
			continue
		}
		value, err := accountIdentityValue(kind, raw)
		if err != nil {
			return errors.New("invalid account identity metadata")
		}
		if value == "" {
			continue
		}
		if kind == "thread" && (name == "thread_id" || name == "thread-id") {
			values[name] = p.ThreadID
		} else {
			mapped, err := p.accountField(kind, value)
			if err != nil {
				return err
			}
			values[name] = mapped
		}
	}
	for _, name := range []string{"turnMetadata", "turn_metadata", "x-codex-turn-metadata"} {
		value, exists := values[name]
		if !exists {
			continue
		}
		var metadata map[string]any
		switch raw := value.(type) {
		case string:
			if len(raw) > 64<<10 || common.UnmarshalPreserveNumbers([]byte(raw), &metadata) != nil || metadata == nil {
				return errors.New("invalid embedded turn metadata")
			}
		case map[string]any:
			metadata = raw
		default:
			return errors.New("invalid embedded turn metadata")
		}
		// Embedded metadata is a flat protocol object, not an arbitrary recursive tree.
		for field, kind := range accountIdentityFields {
			raw, exists := metadata[field]
			if !exists {
				continue
			}
			v, err := accountIdentityValue(kind, raw)
			if err != nil {
				return errors.New("invalid embedded identity field")
			}
			if v != "" {
				if field == "thread_id" || field == "thread-id" {
					metadata[field] = p.ThreadID
				} else {
					mapped, err := p.accountField(kind, v)
					if err != nil {
						return err
					}
					metadata[field] = mapped
				}
			}
		}
		if _, ok := value.(string); ok {
			encoded, err := common.Marshal(metadata)
			if err != nil {
				return err
			}
			values[name] = string(encoded)
		} else {
			values[name] = metadata
		}
	}
	return nil
}

func isModelFacingWindow(value string) bool {
	digits := strings.TrimPrefix(value, "window")
	if digits == value || len(digits) == 0 || len(digits)%3 != 0 {
		return false
	}
	_, err := strconv.ParseUint(digits, 10, 64)
	return err == nil
}

func accountIdentityValue(kind string, raw any) (string, error) {
	if value, ok := raw.(string); ok {
		return value, nil
	}
	if kind == "context-window" {
		encoded, err := common.Marshal(raw)
		var id common.ModelFacingID
		if err == nil && common.Unmarshal(encoded, &id) == nil && id.Salt == "window" {
			return id.String(), nil
		}
	}
	return "", errors.New("invalid identity field type")
}

func validateUniqueAccountMetadata(data []byte, depth int) error {
	if depth > 3 {
		return errors.New("nested turn metadata exceeds limit")
	}
	fields, err := common.DecodeUniqueJSONObject(data)
	if err != nil {
		return err
	}
	for _, name := range []string{"client_metadata", "turnMetadata", "turn_metadata", "x-codex-turn-metadata", "context_window_id", "previous_window_id", "first_window_id"} {
		raw, exists := fields[name]
		if !exists {
			continue
		}
		if common.GetJsonType(raw) == "string" {
			if !strings.Contains(name, "metadata") && name != "turnMetadata" {
				continue
			}
			var value string
			if err := common.Unmarshal(raw, &value); err != nil {
				return err
			}
			raw = []byte(value)
		}
		if err := validateUniqueAccountMetadata(raw, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func applyAccountIdentity(headers http.Header, payload map[string]any, p *AttemptClientProfile) error {
	// Only protocol metadata is projected. User messages and tool results are immutable.
	if err := rewriteAccountMetadata(payload, p); err != nil {
		return err
	}
	rawMetadata, exists := payload["client_metadata"]
	values, ok := rawMetadata.(map[string]any)
	if exists && !ok {
		return errors.New("invalid client metadata")
	}
	if values == nil {
		values = make(map[string]any)
		payload["client_metadata"] = values
	}
	if err := rewriteAccountMetadata(values, p); err != nil {
		return err
	}
	values["x-codex-installation-id"] = p.InstallationID
	values["session_id"], values["thread_id"] = p.SessionID, p.ThreadID
	values["x-codex-window-id"] = p.WindowID
	values["turn_id"] = p.TurnID
	if parent := p.Headers.Get("X-Codex-Parent-Thread-Id"); parent != "" {
		values["x-codex-parent-thread-id"] = parent
	}
	// Preserve operational metadata while producing the identity fields from one
	// snapshot. Direct headers and embedded metadata share the same values.
	metadata := make(map[string]any, len(p.turnMetadata)+8)
	for key, value := range p.turnMetadata {
		metadata[key] = value
	}
	if err := rewriteAccountMetadata(metadata, p); err != nil {
		return err
	}
	metadata["installation_id"], metadata["session_id"], metadata["thread_id"] = p.InstallationID, p.SessionID, p.ThreadID
	metadata["window_id"], metadata["turn_id"], metadata["context_window_id"] = p.WindowID, p.TurnID, p.ContextWindowID
	if _, exists := metadata["root_turn_id"]; !exists {
		metadata["root_turn_id"] = p.TurnID
	}
	if parent := p.Headers.Get("X-Codex-Parent-Thread-Id"); parent != "" {
		metadata["parent_thread_id"] = parent
	}
	encoded, err := common.MarshalASCII(metadata)
	if err != nil || len(encoded) > 64<<10 {
		return errors.New("turn metadata exceeds protocol limit")
	}
	values["x-codex-turn-metadata"] = string(encoded)
	direct := make(map[string]any, len(metadata))
	for name, value := range metadata {
		if name != "tool_namespaces_info" {
			direct[name] = value
		}
	}
	header, err := common.MarshalASCII(direct)
	if err != nil || len(header) > 8096 {
		return errors.New("direct turn metadata exceeds protocol limit")
	}
	headers.Set("X-Codex-Turn-Metadata", string(header))
	for _, name := range []string{"X-Codex-Window-Id", "X-Codex-Parent-Thread-Id", "X-Client-Request-Id"} {
		if raw := headers.Get(name); raw != "" {
			kind := accountIdentityFields[strings.ToLower(name)]
			mapped, err := p.accountField(kind, raw)
			if err != nil {
				return err
			}
			headers.Set(name, mapped)
		}
	}
	if raw, ok := payload["prompt_cache_key"].(string); ok && raw != "" {
		mapped, err := p.correlationID("cache", raw)
		if err != nil {
			return err
		}
		payload["prompt_cache_key"] = mapped
	}
	return nil
}
