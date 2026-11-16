package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

type CodexCredentialRefreshOptions struct {
	ResetCaches bool
	Credential  string // required for multi-key channels; never a positional index
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
	// Older imports pinned an email. Prefer the immutable issuer/subject for
	// account matching, while legacy slot projection keeps its original rule.
	{
		for _, token := range []string{key.IDToken, key.AccessToken} {
			if claims, ok := decodeJWTClaims(token); ok {
				if subject, ok := claims["sub"].(string); ok && subject != "" {
					if issuer, ok := claims["iss"].(string); ok && issuer != "" {
						principal = issuer + "\x00" + subject
					} else if !strings.HasSuffix(principal, "\x00"+subject) {
						principal = subject
					}
					break
				}
			}
		}
	}
	if principal == "" {
		principal = strings.ToLower(strings.TrimSpace(key.Email))
	}
	if key.AccountID == "" || principal == "" {
		return "", errors.New("Codex account and principal are required for stable identity")
	}
	return key.AccountID + "\x00" + principal, nil
}

func legacyCodexCredentialSubject(key *CodexOAuthKey) (string, error) {
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
	if !shouldAutoRefreshCodexChannelStatus(ch.Status) {
		return nil, nil, errors.New("channel does not permit credential refresh")
	}

	prior := ch.Key
	if ch.ChannelInfo.IsMultiKey {
		prior = ""
		for index, key := range ch.GetKeys() {
			if key == opts.Credential {
				if status, exists := ch.ChannelInfo.MultiKeyStatusList[index]; exists && !shouldAutoRefreshCodexChannelStatus(status) {
					return nil, nil, errors.New("credential does not permit refresh")
				}
				prior = key
				break
			}
		}
		if prior == "" {
			return nil, nil, errors.New("select an enabled credential to refresh")
		}
	}
	oauthKey, err := parseCodexOAuthKey(strings.TrimSpace(prior))
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(oauthKey.RefreshToken) == "" {
		return nil, nil, fmt.Errorf("codex channel: refresh_token is required to refresh credential")
	}
	// Preserve the principal already used by device and conversation slots when
	// refresh enriches a previously email-less credential with profile claims.
	subject, subjectErr := legacyCodexCredentialSubject(oauthKey)
	if model.ChannelUsesAccountIdentity(ch) {
		subject, subjectErr = codexCredentialSubject(oauthKey)
	}
	if subjectErr == nil {
		oauthKey.IdentityPrincipal = strings.TrimPrefix(subject, oauthKey.AccountID+"\x00")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	setting := ch.GetSetting()
	var headers http.Header
	if model.ChannelUsesAccountIdentity(ch) {
		headers, err = ChannelMaintenanceHeaders(refreshCtx, ch, prior)
		if err != nil {
			return nil, nil, err
		}
	}
	res, err := refreshCodexOAuthTokenWithProfile(refreshCtx, oauthKey.RefreshToken, HTTPClientOptionsFromChannelSettings(setting), headers, func() error {
		fresh, err := model.GetChannelById(channelID, true)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.Type != ch.Type || !shouldAutoRefreshCodexChannelStatus(fresh.Status) || fresh.ConfigVersion != ch.ConfigVersion {
			return errors.New("channel changed before credential refresh")
		}
		if fresh.ChannelInfo.IsMultiKey {
			for index, key := range fresh.GetKeys() {
				if key == prior {
					if status, exists := fresh.ChannelInfo.MultiKeyStatusList[index]; exists && !shouldAutoRefreshCodexChannelStatus(status) {
						return errors.New("credential disabled before refresh")
					}
					return nil
				}
			}
			return errors.New("credential changed before refresh")
		}
		if fresh.Key != prior {
			return errors.New("credential changed before refresh")
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
	if err := model.ApplyCodexCredentialCAS(persistCtx, ch.Id, prior, string(encoded), ch.ConfigVersion); err != nil {
		return nil, nil, err
	}
	ch, err = model.GetChannelById(ch.Id, true)
	if err != nil {
		return nil, nil, err
	}

	if opts.ResetCaches {
		model.InitChannelCache()
		ResetProxyClientCache()
	}

	return oauthKey, ch, nil
}
