package service

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
)

func accountKeyAvailability(ctx context.Context, channel *model.Channel) (map[int]model.AccountAvailability, error) {
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
	}
	subjects := make([]string, len(keys))
	provider := "codex"
	if channel.Type == constant.ChannelTypeAnthropic {
		provider = "claude"
	}
	for i, key := range keys {
		if status, exists := channel.ChannelInfo.MultiKeyStatusList[i]; exists && status != common.ChannelStatusEnabled {
			continue
		}
		principal, err := upstreamAccountPrincipal(ctx, channel.Type, key, channel.GetSetting())
		if err != nil {
			return nil, err
		}
		subjects[i] = model.AccountSubjectHash(provider, principal)
	}
	snapshot, err := model.AccountAvailabilityBySubject(ctx, subjects)
	if err != nil {
		return nil, err
	}
	result := make(map[int]model.AccountAvailability, len(keys))
	for i, subject := range subjects {
		result[i] = snapshot[subject]
	}
	return result, nil
}

// Affinity is a preference only while at least one enabled account can run.
// The final key selector repeats the check with fresh credentials at dispatch.
func ChannelHasAvailableAccount(ctx context.Context, channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	if !model.ChannelUsesAccountIdentity(channel) {
		return true
	}
	availability, err := accountKeyAvailability(ctx, channel)
	if err != nil {
		common.SysLog("account availability lookup failed")
		return false
	}
	for index, value := range availability {
		status, exists := channel.ChannelInfo.MultiKeyStatusList[index]
		if !value.Blocked && (!exists || status == common.ChannelStatusEnabled) {
			return true
		}
	}
	return false
}

func SelectAvailableAccountKey(ctx context.Context, channel *model.Channel, preferred string, index int, sticky bool, tried map[int]bool) (string, int, *types.NewAPIError) {
	if !model.ChannelUsesAccountIdentity(channel) {
		return preferred, index, nil
	}
	availability, err := accountKeyAvailability(ctx, channel)
	if err != nil {
		return "", 0, admissionError(err)
	}
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
	}
	best := -1
	for i, key := range keys {
		if tried[i] || availability[i].Blocked {
			continue
		}
		if status, ok := channel.ChannelInfo.MultiKeyStatusList[i]; ok && status != common.ChannelStatusEnabled {
			continue
		}
		if sticky && key == preferred {
			return key, i, nil
		}
		if best < 0 || availability[i].Rank < availability[best].Rank || availability[i].Rank == availability[best].Rank && i == index {
			best = i
		}
	}
	if best < 0 {
		return "", 0, admissionError(model.ErrUpstreamAdmissionBusy)
	}
	if keys[best] == "" {
		return "", 0, admissionError(errors.New("empty account credential"))
	}
	return keys[best], best, nil
}
