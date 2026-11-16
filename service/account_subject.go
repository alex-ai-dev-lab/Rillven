package service

import (
	"context"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

func upstreamAccountPrincipal(ctx context.Context, channelType int, key string, settings dto.ChannelSettings) (string, error) {
	if channelType == constant.ChannelTypeCodex {
		credential, err := parseCodexOAuthKey(key)
		if err != nil {
			return "", err
		}
		return codexCredentialSubject(credential)
	}
	if settings.UpstreamAccountSubject != "" {
		return "subject/" + settings.UpstreamAccountSubject, nil
	}
	return model.StableCredentialSubject(ctx, key)
}
