package service

import (
	"runtime"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
)

func AccountClientProfileValues(channelType int, setting dto.ChannelSettings) (string, string, string) {
	if channelType == constant.ChannelTypeAnthropic {
		ua := DefaultClaudeCLIUserAgent
		if setting.ClientVersion != "" {
			ua = "claude-cli/" + setting.ClientVersion + " (external, cli)"
		}
		if setting.UserAgentOverride != "" {
			ua = setting.UserAgentOverride
		}
		return setting.ClientVersion, ua, ""
	}
	return setting.ClientVersion, generatedCodexUserAgent(setting, false), "codex_cli_rs"
}

// Mirrors official login/auth/default_client.rs. The result is pinned once in
// the account record, including platform and terminal, rather than rediscovered
// after deployment. Legacy profiles keep their previously configured value.
func generatedCodexUserAgent(setting dto.ChannelSettings, legacy bool) string {
	if setting.UserAgentOverride != "" {
		return setting.UserAgentOverride
	}
	version := setting.ClientVersion
	if version == "" {
		version = strings.TrimPrefix(DefaultCodexCLIUserAgent, "codex-cli_rs/")
	}
	if legacy && setting.ClientPlatform == "" && setting.ClientTerminal == "" {
		return "codex-cli_rs/" + version
	}
	platform := setting.ClientPlatform
	if platform == "" {
		os := map[string]string{"linux": "Linux", "windows": "Windows", "darwin": "Mac OS"}[runtime.GOOS]
		if os == "" {
			os = runtime.GOOS
		}
		arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
		if arch == "" {
			arch = runtime.GOARCH
		}
		platform = os + " unknown; " + arch
	}
	terminal := setting.ClientTerminal
	if terminal == "" {
		terminal = "unknown"
	}
	return "codex_cli_rs/" + version + " (" + platform + ") " + terminal
}
