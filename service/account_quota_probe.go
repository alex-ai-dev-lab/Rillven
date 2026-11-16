package service

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/google/uuid"
)

func RunAccountQuotaProbes(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var channels []*model.Channel
		if err := model.DB.WithContext(ctx).Where("type = ? AND status = ?", constant.ChannelTypeCodex, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
			common.SysLog("account quota probe scan failed")
			continue
		}
		for _, channel := range channels {
			if !model.ChannelUsesAccountIdentity(channel) {
				continue
			}
			keys := []string{channel.Key}
			if channel.ChannelInfo.IsMultiKey {
				keys = channel.GetKeys()
			}
			for i, selected := range keys {
				if status, ok := channel.ChannelInfo.MultiKeyStatusList[i]; ok && status != common.ChannelStatusEnabled {
					continue
				}
				work, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := probeAccountQuota(work, channel, selected)
				cancel()
				if err != nil && ctx.Err() == nil {
					common.SysLog(fmt.Sprintf("account quota probe failed for channel %d", channel.Id))
				}
				if ctx.Err() != nil {
					return
				}
			}
		}
	}
}

func probeAccountQuota(ctx context.Context, channel *model.Channel, selected string) error {
	credential, err := parseCodexOAuthKey(selected)
	if err != nil {
		return err
	}
	principal, err := codexCredentialSubject(credential)
	if err != nil {
		return err
	}
	var account model.UpstreamAccount
	if err := model.DB.WithContext(ctx).Where("subject_hash = ?", model.AccountSubjectHash("codex", principal)).First(&account).Error; err != nil {
		return err
	}
	interval := time.Duration(max(30, min(common.GetEnvOrDefault("RILLVEN_QUOTA_REFRESH_SECONDS", 60), 3600))) * time.Second
	interval += time.Duration(common.GetRandomInt(11)) * time.Second
	claimed, err := model.ClaimAccountProbe(ctx, account.ID, uuid.NewString(), interval)
	if err != nil || !claimed {
		return err
	}
	profile, err := ChannelMaintenanceHeaders(ctx, channel, selected)
	if err != nil {
		return err
	}
	client, err := GetHttpClientWithChannelSettings(channel.GetSetting())
	if err != nil {
		return err
	}
	observed, err := model.DatabaseMillis(ctx)
	if err != nil {
		return err
	}
	status, body, err := FetchCodexWhamUsage(ctx, client, channel.GetBaseURL(), credential.AccessToken, credential.AccountID, profile)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &CodexOAuthError{Status: status}
	}
	info := &relaycommon.RelayInfo{UpstreamQuotaObservationAt: observed, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ApiKey: selected, ChannelBaseUrl: channel.GetBaseURL(), ChannelSetting: channel.GetSetting()}}
	ObserveCodexAccountQuota(ctx, info, nil, body)
	return nil
}
