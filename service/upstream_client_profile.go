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
// no credentials and is never reused for a different channel/slot.
type AttemptClientProfile struct {
	Mode, Provider, SlotID                        string
	InstallationID, DeviceID, SessionID, ThreadID string
	WindowID, TurnID, ContextWindowID             string
	IdentityRevision                              int64
	Headers                                       http.Header
	AccountID                                     string
	AccountRevision                               int64
	seed                                          []byte
	owner                                         string
	mapped                                        map[string]struct{}
	correlations                                  map[string]string
	turnMetadata                                  map[string]any
	ctx                                           context.Context
}

func ScopedIdentityMode(mode string) bool { return mode == "per_channel" || mode == "per_account" }

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
	if info != nil && info.ChannelMeta != nil {
		if mode := info.ChannelSetting.ClientIdentityMode; mode != "" && mode != "inherit" {
			return mode
		}
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

func UsesAccountIdentity(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ChannelMeta != nil && channelIdentityScope(info) == "per_account"
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
		session = gjson.GetBytes(body, "client_metadata.session_id").String()
	}
	if thread == "" {
		thread = gjson.GetBytes(body, "client_metadata.thread_id").String()
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
	p := &AttemptClientProfile{Mode: mode, Headers: make(http.Header), ctx: c.Request.Context()}
	if mode == "disabled" {
		return p, nil
	}
	if !ScopedIdentityMode(mode) && mode != "shadow" {
		return nil, errors.New("invalid channel identity scope")
	}
	if strings.Contains(path, "/messages") {
		p.Provider = "claude"
	} else if forceCodex || strings.Contains(path, "/responses") {
		p.Provider = "codex"
	} else {
		return p, nil
	}
	row, err := model.EnsureChannelIdentity(c.Request.Context(), info.ChannelId, mode == "per_account")
	if err != nil {
		return nil, err
	}
	secret, err := row.Secret()
	if err != nil {
		return nil, err
	}
	legacyExists := !secret.CreatedForAccount
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
		subject, err = legacyCodexCredentialSubject(key)
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
	if mode == "per_account" {
		subject, err = upstreamAccountPrincipal(c.Request.Context(), info.ChannelType, info.ApiKey, info.ChannelSetting)
		if err != nil {
			return nil, err
		}
		var bound int64
		if err := model.DB.WithContext(c.Request.Context()).Model(&model.ChannelAccountBinding{}).
			Joins("JOIN upstream_accounts ON upstream_accounts.id = channel_account_bindings.account_id").
			Where("channel_account_bindings.channel_id = ? AND upstream_accounts.subject_hash = ?", info.ChannelId, model.AccountSubjectHash(p.Provider, subject)).Count(&bound).Error; err != nil {
			return nil, err
		}
		version := info.ChannelSetting.ClientVersion
		ua, originator := DefaultCodexCLIUserAgent, "codex_cli_rs"
		if p.Provider == "claude" {
			ua, originator = DefaultClaudeCLIUserAgent, ""
		}
		if version != "" {
			if p.Provider == "codex" {
				ua = "codex-cli_rs/" + version
			} else {
				ua = "claude-cli/" + version + " (external, cli)"
			}
		}
		if p.Provider == "codex" {
			ua = generatedCodexUserAgent(info.ChannelSetting, legacyExists)
		}
		initial := model.AccountIdentitySecret{Seed: secret.Seed, LegacySlot: slot.ID, LegacyRevision: row.Revision, InstallationID: p.InstallationID, DeviceID: p.DeviceID, ClientVersion: version, UserAgent: ua, Originator: originator}
		initial.NewInstallation = !legacyExists && secret.InstallationID == ""
		initial.ExplicitProfile = info.ChannelSetting.ClientPlatform != "" || info.ChannelSetting.ClientTerminal != "" || info.ChannelSetting.UserAgentOverride != ""
		account, err := model.EnsureUpstreamAccount(c.Request.Context(), p.Provider, subject, initial, info.ChannelSetting.UpstreamAccountID, legacyExists && bound == 0, info.ClientProfilePreview)
		if err != nil {
			return nil, err
		}
		if err := model.BindChannelAccount(c.Request.Context(), info.ChannelId, slot.ID, account.ID); err != nil {
			return nil, err
		}
		identity, err := account.Secret()
		if err != nil {
			return nil, err
		}
		seed, err = base64.StdEncoding.DecodeString(identity.Seed)
		if err != nil {
			return nil, err
		}
		p.AccountID, p.SlotID, p.IdentityRevision = account.ID, identity.LegacySlot, identity.LegacyRevision+account.IdentityRevision-1
		p.AccountRevision = account.Revision
		p.InstallationID, p.DeviceID = identity.InstallationID, identity.DeviceID
		p.Headers.Set("User-Agent", identity.UserAgent)
		if identity.Originator != "" {
			p.Headers.Set("Originator", identity.Originator)
		}
	}
	session, thread := callerConversation(c.Request.Header, body)
	var accountInput map[string]string
	if mode == "per_account" && p.Provider == "codex" {
		if turn := GetConversationTurn(c); turn != nil {
			if _, err := collectAccountIdentity(c.Request.Header, turn.Original); err != nil {
				return nil, err
			}
		}
		accountInput, err = collectAccountIdentity(c.Request.Header, body)
		if err != nil {
			return nil, err
		}
		p.turnMetadata, err = collectAccountTurnMetadata(c.Request.Header, body)
		if err != nil {
			return nil, err
		}
		session, thread = accountInput["session_id"], accountInput["thread_id"]
		if session == "" {
			session = thread
		}
	}
	if logical := c.GetString(ContextConversationSession); logical != "" && session == "" {
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
	p.seed, p.owner = seed, owner
	revision := fmt.Sprint(p.IdentityRevision)
	p.SessionID = identityUUID(seed, p.Provider, p.SlotID, owner, session, revision, "session")
	if thread == "" {
		thread = session
	}
	p.ThreadID = identityUUID(seed, p.Provider, p.SlotID, owner, session, thread, revision, "thread")
	if mode == "per_account" {
		p.ThreadID, err = p.correlationID("thread", thread)
		if err != nil {
			return nil, err
		}
		// A root session is its root thread; descendants carry their root's ID.
		p.SessionID, err = p.correlationID("thread", session)
		if err != nil {
			return nil, err
		}
		if p.Provider == "codex" {
			requestID := info.RequestId
			if requestID == "" {
				requestID = c.GetString("identity_request_id")
				if requestID == "" {
					requestID = uuid.NewString()
					c.Set("identity_request_id", requestID)
				}
			}
			if err := p.resolveCodexCorrelations(accountInput, thread, requestID); err != nil {
				return nil, err
			}
		}
	}
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
		if mode == "per_account" {
			p.Headers.Set("X-Client-Request-Id", p.ThreadID)
		}
	} else {
		p.Headers.Set("X-Claude-Code-Session-Id", p.SessionID)
	}
	if preset := info.ChannelSetting.ClientProfile; preset != "" && preset != "inherit" {
		if preset != p.Provider {
			return nil, errors.New("client profile conflicts with upstream protocol")
		}
		if mode == "per_account" {
			return p, nil
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
		if p.Mode == "per_account" {
			return applyAccountIdentity(headers, payload, p)
		}
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
	if p == nil || !ScopedIdentityMode(p.Mode) {
		return nil
	}
	if p.Mode == "per_account" {
		for _, name := range []string{"X-Codex-Window-Id", "X-Codex-Parent-Thread-Id", "X-Client-Request-Id"} {
			if raw := headers.Get(name); raw != "" {
				kind := accountIdentityFields[strings.ToLower(name)]
				value, err := p.accountField(kind, raw)
				if err != nil {
					return err
				}
				headers.Set(name, value)
			}
		}
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
		if err := common.UnmarshalPreserveNumbers([]byte(headers.Get("X-Codex-Turn-Metadata")), &metadata); err != nil || metadata == nil {
			return errors.New("invalid Codex turn metadata")
		}
		if p.Mode == "per_account" {
			if err := rewriteAccountMetadata(metadata, p); err != nil {
				return err
			}
		}
		for field, value := range map[string]string{"session_id": p.SessionID, "thread_id": p.ThreadID, "x-codex-installation-id": p.InstallationID} {
			if _, exists := metadata[field]; exists {
				metadata[field] = value
			}
		}
		encoded, err := common.MarshalASCII(metadata)
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
