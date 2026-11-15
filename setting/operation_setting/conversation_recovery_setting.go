package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

type ConversationRecoverySetting struct {
	IdleTTLSeconds     int `json:"idle_ttl_seconds"`
	MaxLifetimeSeconds int `json:"max_lifetime_seconds"`
	MaxReplayBytes     int `json:"max_replay_bytes"`
	MaxSessionBytes    int `json:"max_session_bytes"`
	MaxOwnerBytes      int `json:"max_owner_bytes"`
	MaxDepth           int `json:"max_depth"`
	MaxAttempts        int `json:"max_attempts"`
	MaxBufferBytes     int `json:"max_buffer_bytes"`
	GlobalBufferBytes  int `json:"global_buffer_bytes"`
}

var conversationRecoverySetting = ConversationRecoverySetting{
	IdleTTLSeconds: 86400, MaxLifetimeSeconds: 604800,
	MaxReplayBytes: 16 << 20, MaxSessionBytes: 64 << 20, MaxOwnerBytes: 512 << 20,
	MaxDepth: 128, MaxAttempts: 3, MaxBufferBytes: 16 << 20, GlobalBufferBytes: 256 << 20,
}

func init() {
	config.GlobalConfig.Register("conversation_recovery_setting", &conversationRecoverySetting)
}

func GetConversationRecoverySetting() ConversationRecoverySetting {
	s := config.ReadValue[ConversationRecoverySetting]("conversation_recovery_setting")
	// Hard bounds also protect startup against old/manual option writes that did
	// not pass current API validation. Zero must never mean unlimited retention.
	s.MaxLifetimeSeconds = max(60, min(s.MaxLifetimeSeconds, 30*86400))
	s.IdleTTLSeconds = max(60, min(s.IdleTTLSeconds, s.MaxLifetimeSeconds))
	s.MaxReplayBytes = max(1024, min(s.MaxReplayBytes, 64<<20))
	s.MaxSessionBytes = max(s.MaxReplayBytes, min(s.MaxSessionBytes, 512<<20))
	s.MaxOwnerBytes = max(s.MaxSessionBytes, min(s.MaxOwnerBytes, 2<<30))
	s.MaxDepth = max(1, min(s.MaxDepth, 512))
	s.MaxAttempts = max(1, min(s.MaxAttempts, 6))
	s.MaxBufferBytes = max(1024, min(s.MaxBufferBytes, 64<<20))
	s.GlobalBufferBytes = max(s.MaxBufferBytes, min(s.GlobalBufferBytes, 1<<30))
	return s
}
