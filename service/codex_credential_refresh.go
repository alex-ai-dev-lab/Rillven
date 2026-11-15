package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

type CodexCredentialRefreshOptions struct {
	ResetCaches bool
}

type CodexOAuthKey struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`

	AccountID         string `json:"account_id,omitempty"`
	LastRefresh       string `json:"last_refresh,omitempty"`
	Email             string `json:"email,omitempty"`
	Type              string `json:"type,omitempty"`
	Expired           string `json:"expired,omitempty"`
	IdentityPrincipal string `json:"identity_principal,omitempty"`
}

func codexCredentialSubject(key *CodexOAuthKey) (string, error) {
	principal := key.IdentityPrincipal
	if principal == "" {
		principal = strings.ToLower(strings.TrimSpace(key.Email))
	}
	if principal == "" {
		for _, token := range []string{key.IDToken, key.AccessToken} {
			if claims, ok := decodeJWTClaims(token); ok {
				if subject, ok := claims["sub"].(string); ok && subject != "" {
					principal = subject
					break
				}
			}
		}
	}
	if key.AccountID == "" || principal == "" {
		return "", errors.New("Codex account and principal are required for stable identity")
	}
	return key.AccountID + "\x00" + principal, nil
}

func parseCodexOAuthKey(raw string) (*CodexOAuthKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("codex channel: empty oauth key")
	}
	var key CodexOAuthKey
	if err := common.Unmarshal([]byte(raw), &key); err != nil {
		return nil, errors.New("codex channel: invalid oauth key json")
	}
	return &key, nil
}

func RefreshCodexChannelCredential(ctx context.Context, channelID int, opts CodexCredentialRefreshOptions) (*CodexOAuthKey, *model.Channel, error) {
	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	if ch == nil {
		return nil, nil, fmt.Errorf("channel not found")
	}
	if ch.Type != constant.ChannelTypeCodex {
		return nil, nil, fmt.Errorf("channel type is not Codex")
	}
	if ch.ChannelInfo.IsMultiKey || !shouldAutoRefreshCodexChannelStatus(ch.Status) {
		return nil, nil, errors.New("channel does not permit credential refresh")
	}

	oauthKey, err := parseCodexOAuthKey(strings.TrimSpace(ch.Key))
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(oauthKey.RefreshToken) == "" {
		return nil, nil, fmt.Errorf("codex channel: refresh_token is required to refresh credential")
	}
	// Preserve the principal already used by device and conversation slots when
	// refresh enriches a previously email-less credential with profile claims.
	if subject, subjectErr := codexCredentialSubject(oauthKey); subjectErr == nil {
		oauthKey.IdentityPrincipal = strings.TrimPrefix(subject, oauthKey.AccountID+"\x00")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	setting := ch.GetSetting()
	res, err := refreshCodexOAuthTokenWithCheck(refreshCtx, oauthKey.RefreshToken, HTTPClientOptionsFromChannelSettings(setting), func() error {
		fresh, err := model.GetChannelById(channelID, true)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.Type != ch.Type || fresh.ChannelInfo.IsMultiKey || !shouldAutoRefreshCodexChannelStatus(fresh.Status) || fresh.Key != ch.Key || fresh.ConfigVersion != ch.ConfigVersion {
			return errors.New("channel changed before credential refresh")
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	oauthKey.AccessToken = res.AccessToken
	oauthKey.RefreshToken = res.RefreshToken
	oauthKey.LastRefresh = time.Now().Format(time.RFC3339)
	oauthKey.Expired = res.ExpiresAt.Format(time.RFC3339)
	if strings.TrimSpace(oauthKey.Type) == "" {
		oauthKey.Type = "codex"
	}

	if strings.TrimSpace(oauthKey.AccountID) == "" {
		if accountID, ok := ExtractCodexAccountIDFromJWT(oauthKey.AccessToken); ok {
			oauthKey.AccountID = accountID
		}
	}
	if strings.TrimSpace(oauthKey.Email) == "" {
		if email, ok := ExtractEmailFromJWT(oauthKey.AccessToken); ok {
			oauthKey.Email = email
		}
	}

	encoded, err := common.Marshal(oauthKey)
	if err != nil {
		return nil, nil, err
	}

	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer persistCancel()
	if err := model.ApplyCodexCredentialCAS(persistCtx, ch.Id, ch.Key, string(encoded), ch.ConfigVersion); err != nil {
		return nil, nil, err
	}
	ch.Key = string(encoded)

	if opts.ResetCaches {
		model.InitChannelCache()
		ResetProxyClientCache()
	}

	return oauthKey, ch, nil
}
