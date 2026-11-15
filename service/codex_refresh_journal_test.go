package service

import (
	"context"
	"encoding/base64"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestCodexRefreshJournalSerializesSharedGrantAndRetainsReceipt(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CodexRefreshJournal{}))
	var calls atomic.Int32
	started, finish := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := withCodexRefreshJournal(ctx, "shared-grant", func() (*CodexOAuthTokenResult, error) {
			calls.Add(1)
			close(started)
			<-finish
			return &CodexOAuthTokenResult{AccessToken: "access-new", RefreshToken: "grant-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
		})
		done <- err
	}()
	<-started
	_, err := withCodexRefreshJournal(context.Background(), "shared-grant", func() (*CodexOAuthTokenResult, error) { calls.Add(1); return nil, errors.New("unexpected refresh") })
	require.ErrorIs(t, err, model.ErrRefreshGrantUnavailable)
	cancel()
	close(finish)
	require.NoError(t, <-done)
	result, err := withCodexRefreshJournal(context.Background(), "shared-grant", func() (*CodexOAuthTokenResult, error) { calls.Add(1); return nil, errors.New("unexpected refresh") })
	require.NoError(t, err)
	require.Equal(t, "grant-new", result.RefreshToken)
	require.EqualValues(t, 1, calls.Load())
	var row model.CodexRefreshJournal
	require.NoError(t, model.DB.First(&row).Error)
	require.NotContains(t, row.Ciphertext, "grant-new")
}

func TestCodexRefreshBeforeSendRejectionReleasesUnsentGrant(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CodexRefreshJournal{}))
	changed := errors.New("channel configuration changed")
	_, err := withCodexRefreshJournal(context.Background(), "unsent", func() (*CodexOAuthTokenResult, error) {
		t.Fatal("must check channel before sending")
		return nil, nil
	}, func() error { return changed })
	require.ErrorIs(t, err, changed)
	_, err = withCodexRefreshJournal(context.Background(), "unsent", func() (*CodexOAuthTokenResult, error) {
		return &CodexOAuthTokenResult{AccessToken: "new-access", RefreshToken: "new-grant"}, nil
	})
	require.NoError(t, err, "an unsent grant remains usable")
}

func TestCodexRefreshPreservesPrincipalAndHonorsConfigVersion(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CodexRefreshJournal{}, &model.Channel{}))
	token := func(claims string) string {
		return "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
	}
	old := &CodexOAuthKey{AccountID: "account", AccessToken: token(`{"sub":"subject"}`), RefreshToken: "old-grant"}
	before, err := codexCredentialSubject(old)
	require.NoError(t, err)
	key, err := common.Marshal(old)
	require.NoError(t, err)
	channel := &model.Channel{Id: 1, Type: constant.ChannelTypeCodex, Status: common.ChannelStatusEnabled, Key: string(key), ConfigVersion: 7}
	require.NoError(t, model.DB.Create(channel).Error)
	// Recover a durable rotation receipt through the real channel refresh path.
	_, err = withCodexRefreshJournal(context.Background(), old.RefreshToken, func() (*CodexOAuthTokenResult, error) {
		return &CodexOAuthTokenResult{AccessToken: token(`{"sub":"subject","email":"new@example.test"}`), RefreshToken: "new-grant", ExpiresAt: time.Now().Add(time.Hour)}, nil
	})
	require.NoError(t, err)
	fresh, saved, err := RefreshCodexChannelCredential(context.Background(), channel.Id, CodexCredentialRefreshOptions{})
	require.NoError(t, err)
	require.Equal(t, "new@example.test", fresh.Email)
	after, err := codexCredentialSubject(fresh)
	require.NoError(t, err)
	require.Equal(t, before, after, "adding email must not rotate credential/device identity")
	require.NoError(t, model.DB.Model(channel).Update("config_version", 8).Error)
	require.Error(t, model.ApplyCodexCredentialCAS(context.Background(), channel.Id, saved.Key, "stale-refresh", 7))
	require.NoError(t, model.DB.First(channel, channel.Id).Error)
	require.Equal(t, saved.Key, channel.Key, "old refresh must not overwrite a newer channel configuration")
}

func TestCodexRefreshJournalUnknownResultNeverReplaysExpiredLease(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.CodexRefreshJournal{}, &model.Channel{}))
	_, err := withCodexRefreshJournal(context.Background(), "lost-grant", func() (*CodexOAuthTokenResult, error) { return nil, errors.New("response lost after request sent") })
	require.Error(t, err)
	require.NoError(t, model.DB.Model(&model.CodexRefreshJournal{}).Where("state = ?", "unknown").Update("lease_until", 1).Error)
	_, err = withCodexRefreshJournal(context.Background(), "lost-grant", func() (*CodexOAuthTokenResult, error) { t.Fatal("must not replay unknown rotation"); return nil, nil })
	require.ErrorIs(t, err, model.ErrRefreshGrantUnavailable)
	channel := &model.Channel{Id: 1, Status: common.ChannelStatusManuallyDisabled, Key: "old"}
	require.NoError(t, model.DB.Create(channel).Error)
	require.Error(t, model.ApplyCodexCredentialCAS(context.Background(), 1, "old", "new"))
	require.NoError(t, model.DB.Model(channel).Updates(map[string]any{"status": common.ChannelStatusEnabled, "key": "admin-replaced"}).Error)
	require.Error(t, model.ApplyCodexCredentialCAS(context.Background(), 1, "old", "new"))
	require.NoError(t, model.DB.First(channel, 1).Error)
	require.Equal(t, "admin-replaced", channel.Key)
	require.NoError(t, model.ApplyCodexCredentialCAS(context.Background(), 1, "admin-replaced", "new"))
	require.NoError(t, model.DB.First(channel, 1).Error)
	require.Equal(t, "new", channel.Key)
}
