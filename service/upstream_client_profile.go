package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	DefaultCodexCLIUserAgent   = "codex-cli_rs/0.59.0"
	DefaultClaudeCLIUserAgent  = "claude-cli/2.1.80 (external, cli)"
	ContextConversationSession = "managed_conversation_session"
)

// AttemptClientProfile is resolved after selecting the credential. It contains
// no credentials or seeds and is never reused for a different channel/slot.
type AttemptClientProfile struct {
	Mode, Provider, SlotID                        string
	InstallationID, DeviceID, SessionID, ThreadID string
	IdentityRevision                              int64
	Headers                                       http.Header
}

func identityProjection(seed []byte, fields ...string) []byte {
	h := hmac.New(sha256.New, seed)
	var size [4]byte
	for _, field := range fields {
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(field))
	}
	return h.Sum(nil)
}

func identityUUID(seed []byte, fields ...string) string {
	b := identityProjection(seed, fields...)[:16]
	b[6], b[8] = (b[6]&0x0f)|0x40, (b[8]&0x3f)|0x80
	id, _ := uuid.FromBytes(b)
	return id.String()
}

func channelIdentityScope(info *relaycommon.RelayInfo) string {
	if mode := info.ChannelSetting.ClientIdentityMode; mode != "" && mode != "inherit" {
		return mode
	}
	setting := model.GetClientIdentitySetting()
	if !setting.Enabled {
		return "disabled"
	}
	if setting.Scope == "" {
		return "legacy"
	}
	return setting.Scope
}

func callerConversation(headers http.Header, body []byte) (session, thread string) {
	for _, key := range []string{"Session-Id", "Session_id", "X-Claude-Code-Session-Id"} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			session = value
			break
		}
	}
	for _, key := range []string{"Thread-Id", "Thread_id", "X-Codex-Thread-Id"} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			thread = value
			break
		}
	}
	if session == "" {
		user := gjson.GetBytes(body, "metadata.user_id")
		if user.Type == gjson.String {
			user = gjson.Parse(user.String())
		}
		session = user.Get("session_id").String()
	}
	return session, thread
}

func ResolveAttemptClientProfile(c *gin.Context, info *relaycommon.RelayInfo, path string, body []byte, forceCodex bool) (*AttemptClientProfile, error) {
	mode := channelIdentityScope(info)
	if mode == "legacy" {
		return nil, nil
	}
	p := &AttemptClientProfile{Mode: mode, Headers: make(http.Header)}
	if mode == "disabled" {
		return p, nil
	}
	if mode != "per_channel" && mode != "shadow" {
		return nil, errors.New("invalid channel identity scope")
	}
	if strings.Contains(path, "/messages") {
		p.Provider = "claude"
	} else if forceCodex || strings.Contains(path, "/responses") {
		p.Provider = "codex"
	} else {
		return p, nil
	}
	row, err := model.EnsureChannelIdentity(c.Request.Context(), info.ChannelId)
	if err != nil {
		return nil, err
	}
	secret, err := row.Secret()
	if err != nil {
		return nil, err
	}
	seed, _ := base64.StdEncoding.DecodeString(secret.Seed)
	slotSeed, err := base64.StdEncoding.DecodeString(secret.SlotSeed)
	if err != nil || len(slotSeed) != 32 {
		return nil, errors.New("invalid credential slot seed")
	}
	subject := info.ApiKey
	if info.ChannelType == constant.ChannelTypeCodex {
		key, err := parseCodexOAuthKey(info.ApiKey)
		if err != nil {
			return nil, err
		}
		subject, err = codexCredentialSubject(key)
		if err != nil {
			return nil, err
		}
	}
	if subject == "" {
		return nil, errors.New("credential is unavailable for channel identity")
	}
	subjectHash := hex.EncodeToString(identityProjection(slotSeed, p.Provider, subject))
	slot, err := model.EnsureChannelCredentialSlot(c.Request.Context(), info.ChannelId, subjectHash)
	if err != nil {
		return nil, err
	}
	p.SlotID, p.IdentityRevision = slot.ID, row.Revision
	p.InstallationID = identityUUID(seed, p.Provider, slot.ID, "installation")
	p.DeviceID = hex.EncodeToString(identityProjection(seed, p.Provider, slot.ID, "device"))
	// A channel-wide explicit ID is meaningful for a single credential. Multi-key
	// channels retain per-slot identities so two accounts never share that override.
	if !info.ChannelIsMultiKey {
		if secret.InstallationID != "" {
			p.InstallationID = secret.InstallationID
		}
		if secret.DeviceID != "" {
			p.DeviceID = secret.DeviceID
		}
	}
	session, thread := callerConversation(c.Request.Header, body)
	if logical := c.GetString(ContextConversationSession); logical != "" {
		session = logical
	}
	if session == "" {
		requestID := info.RequestId
		if requestID == "" {
			requestID = c.GetString("identity_request_id")
			if requestID == "" {
				requestID = uuid.NewString()
				c.Set("identity_request_id", requestID)
			}
		}
		session = "request:" + requestID
	}
	owner := fmt.Sprintf("%d/%d/%x", info.UserId, info.TokenId, sha256.Sum256([]byte(info.TokenKey)))
	revision := fmt.Sprint(row.Revision)
	p.SessionID = identityUUID(seed, p.Provider, slot.ID, owner, session, revision, "session")
	if thread == "" {
		thread = session
	}
	p.ThreadID = identityUUID(seed, p.Provider, slot.ID, owner, session, thread, revision, "thread")
	if mode == "shadow" {
		return p, nil
	}
	if p.Provider == "codex" {
		p.Headers.Set("Session-Id", p.SessionID)
		p.Headers.Set("Session_id", p.SessionID)
		p.Headers.Set("Thread-Id", p.ThreadID)
		p.Headers.Set("Thread_id", p.ThreadID)
		p.Headers.Set("X-Codex-Thread-Id", p.ThreadID)
		p.Headers.Set("X-Codex-Installation-Id", p.InstallationID)
	} else {
		p.Headers.Set("X-Claude-Code-Session-Id", p.SessionID)
	}
	if preset := info.ChannelSetting.ClientProfile; preset != "" && preset != "inherit" {
		if preset != p.Provider {
			return nil, errors.New("client profile conflicts with upstream protocol")
		}
		version := info.ChannelSetting.ClientVersion
		if preset == "codex" {
			ua := DefaultCodexCLIUserAgent
			if version != "" {
				ua = "codex-cli_rs/" + version
			}
			p.Headers.Set("User-Agent", ua)
			p.Headers.Set("Originator", "codex_cli_rs")
		} else {
			ua := DefaultClaudeCLIUserAgent
			if version != "" {
				ua = "claude-cli/" + version + " (external, cli)"
			}
			p.Headers.Set("User-Agent", ua)
			p.Headers.Set("X-App", "cli")
		}
	}
	return p, nil
}

func applyScopedClientIdentity(headers http.Header, payload map[string]any, p *AttemptClientProfile) error {
	if err := EnforceAttemptIdentityHeaders(headers, p); err != nil {
		return err
	}
	if p.Provider == "codex" {
		metadata, _ := payload["client_metadata"].(map[string]any)
		if metadata == nil {
			metadata = make(map[string]any)
			payload["client_metadata"] = metadata
		}
		metadata["x-codex-installation-id"] = p.InstallationID
		for _, field := range []string{"session_id", "thread_id"} {
			if _, exists := metadata[field]; exists {
				if field == "session_id" {
					metadata[field] = p.SessionID
				} else {
					metadata[field] = p.ThreadID
				}
			}
		}
	} else if p.Provider == "claude" {
		metadata, _ := payload["metadata"].(map[string]any)
		if metadata == nil {
			metadata = make(map[string]any)
			payload["metadata"] = metadata
		}
		user := make(map[string]any)
		switch raw := metadata["user_id"].(type) {
		case string:
			_ = common.Unmarshal([]byte(raw), &user)
		case map[string]any:
			user = raw
		}
		if user == nil {
			user = make(map[string]any)
		}
		user["device_id"], user["session_id"] = p.DeviceID, p.SessionID
		raw, err := common.Marshal(user)
		if err != nil {
			return err
		}
		metadata["user_id"] = string(raw)
	}
	return nil
}

func EnforceAttemptIdentityHeaders(headers http.Header, p *AttemptClientProfile) error {
	if p == nil || p.Mode != "per_channel" {
		return nil
	}
	for name, values := range p.Headers {
		for existing := range headers {
			if strings.EqualFold(existing, name) {
				delete(headers, existing)
			}
		}
		headers[name] = append([]string(nil), values...)
	}
	if p.Provider == "codex" && headers.Get("X-Codex-Turn-Metadata") != "" {
		var metadata map[string]any
		if err := common.Unmarshal([]byte(headers.Get("X-Codex-Turn-Metadata")), &metadata); err != nil || metadata == nil {
			return errors.New("invalid Codex turn metadata")
		}
		for field, value := range map[string]string{"session_id": p.SessionID, "thread_id": p.ThreadID, "x-codex-installation-id": p.InstallationID} {
			if _, exists := metadata[field]; exists {
				metadata[field] = value
			}
		}
		encoded, err := common.Marshal(metadata)
		if err != nil {
			return err
		}
		headers.Set("X-Codex-Turn-Metadata", string(encoded))
	}
	return nil
}

// Maintenance uses a separate session and the same credential device identity.
func ChannelMaintenanceHeaders(ctx context.Context, channel *model.Channel, selectedCredential ...string) (http.Header, error) {
	if channel == nil || channel.Id <= 0 {
		return nil, errors.New("save the channel before resolving its identity")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "/maintenance", nil)
	if err != nil {
		return nil, err
	}
	c := &gin.Context{Request: req}
	info := &relaycommon.RelayInfo{RequestId: "maintenance", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ApiKey: channel.Key, ChannelSetting: channel.GetSetting(), ChannelIsMultiKey: channel.ChannelInfo.IsMultiKey}}
	if len(selectedCredential) > 0 {
		info.ApiKey = selectedCredential[0]
	} else if channel.ChannelInfo.IsMultiKey {
		return nil, errors.New("select one credential for channel maintenance")
	}
	path := "/maintenance"
	if channel.Type == constant.ChannelTypeAnthropic {
		path = "/v1/messages"
	}
	forceCodex := channel.Type == constant.ChannelTypeCodex || channel.Type == constant.ChannelTypeOpenAI || info.ChannelSetting.ClientProfile == "codex" || info.ChannelSetting.RequiresCodexIdentity != nil && *info.ChannelSetting.RequiresCodexIdentity
	profile, err := ResolveAttemptClientProfile(c, info, path, []byte(`{}`), forceCodex)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, nil
	}
	return profile.Headers, nil
}
