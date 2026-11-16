package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountIdentitySharedChannelsIndependentOwnersAndLineage(t *testing.T) {
	setupIdentityStateTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("Session-Id", "client-session")
	info := &relaycommon.RelayInfo{UserId: 1, TokenId: 1, TokenKey: "user-one", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, ApiKey: "account-a", ChannelSetting: dto.ChannelSettings{ClientIdentityMode: "per_account"}}}
	resolve := func(body string) *AttemptClientProfile {
		p, err := ResolveAttemptClientProfile(c, info, "/v1/responses", []byte(body), false)
		require.NoError(t, err)
		return p
	}
	a := resolve(`{"client_metadata":{"thread_id":"parent"}}`)
	info.ChannelId = 2
	copy := resolve(`{"client_metadata":{"thread_id":"parent"}}`)
	require.Equal(t, a.AccountID, copy.AccountID)
	require.Equal(t, a.InstallationID, copy.InstallationID)
	require.Equal(t, a.SessionID, copy.SessionID)
	require.Equal(t, a.ThreadID, copy.ThreadID)
	again := resolve(`{"client_metadata":{"thread_id":"parent"}}`)
	require.Equal(t, copy.InstallationID, again.InstallationID)
	info.TokenId = 2
	info.TokenKey = "user-two"
	other := resolve(`{"client_metadata":{"thread_id":"parent"}}`)
	require.Equal(t, a.InstallationID, other.InstallationID)
	require.NotEqual(t, a.SessionID, other.SessionID)
	require.NotEqual(t, a.ThreadID, other.ThreadID)
	info.TokenId = 1
	info.TokenKey = "user-one"
	child := resolve(`{"client_metadata":{"thread_id":"child"}}`)
	raw := `{"input":"parent should remain literal","client_metadata":{"installation_id":"foreign","thread_id":"child","parent_thread_id":"parent","window_id":"parent:2","x-codex-turn-metadata":"{\"parent_thread_id\":\"parent\",\"installation_id\":\"foreign\"}"}}`
	body, headers, err := ApplyClientIdentityToRequestBytesWithOptions("/v1/responses", c.Request.Header, []byte(raw), ClientIdentityOptions{Profile: child})
	require.NoError(t, err)
	require.Equal(t, a.InstallationID, gjson.GetBytes(body, "client_metadata.installation_id").String())
	require.Equal(t, a.ThreadID, gjson.GetBytes(body, "client_metadata.parent_thread_id").String())
	require.Equal(t, a.ThreadID+":2", gjson.GetBytes(body, "client_metadata.window_id").String())
	require.Equal(t, "parent should remain literal", gjson.GetBytes(body, "input").String())
	require.Equal(t, a.InstallationID, headers.Get("X-Codex-Installation-Id"))
	require.Equal(t, generatedCodexUserAgent(info.ChannelSetting, false), headers.Get("User-Agent"))
	require.NoError(t, EnforceAttemptIdentityHeaders(headers, child))
	info.ChannelId = 3
	info.ApiKey = "account-b"
	b := resolve(`{}`)
	require.NotEqual(t, a.InstallationID, b.InstallationID)
	require.NotEqual(t, a.SessionID, b.SessionID)
}

func TestAccountProfileUpgradeKeepsInstallationAndThreadMapping(t *testing.T) {
	setupIdentityStateTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("Session-Id", "root")
	info := &relaycommon.RelayInfo{UserId: 1, TokenId: 1, TokenKey: "owner", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, ApiKey: "key", ChannelSetting: dto.ChannelSettings{ClientIdentityMode: "per_account", ClientVersion: "0.59.0"}}}
	a, err := ResolveAttemptClientProfile(c, info, "/v1/responses", []byte(`{}`), false)
	require.NoError(t, err)
	version, ua, originator := AccountClientProfileValues(info.ChannelType, dto.ChannelSettings{ClientVersion: "0.60.0"})
	require.NoError(t, model.UpdateUpstreamAccountProfile(t.Context(), a.AccountID, a.AccountRevision, version, ua, originator))
	_, err = ResolveAttemptClientProfile(c, info, "/v1/responses", []byte(`{}`), false)
	require.Error(t, err, "old channel profile must not undo an explicit upgrade")
	info.ChannelSetting.ClientVersion = version
	b, err := ResolveAttemptClientProfile(c, info, "/v1/responses", []byte(`{}`), false)
	require.NoError(t, err)
	require.Equal(t, a.InstallationID, b.InstallationID)
	require.Equal(t, a.SessionID, b.SessionID)
	require.Equal(t, a.ThreadID, b.ThreadID)
	require.Equal(t, ua, b.Headers.Get("User-Agent"))
	require.Equal(t, a.IdentityRevision, b.IdentityRevision)
	require.Greater(t, b.AccountRevision, a.AccountRevision)
}

func TestAccountSelectionIgnoresInvalidDisabledCredential(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateAccountQuota())
	require.NoError(t, model.MigrateUpstreamAdmission())
	setting := `{"client_identity_mode":"per_account"}`
	valid := `{"account_id":"account","email":"member@example.test","access_token":"access"}`
	channel := &model.Channel{Type: constant.ChannelTypeCodex, Key: "invalid\n" + valid, Setting: &setting, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled}}}
	key, index, err := SelectAvailableAccountKey(t.Context(), channel, valid, 1, true, nil)
	require.Nil(t, err)
	require.Equal(t, valid, key)
	require.Equal(t, 1, index)
}

func TestAccountCredentialVaultRotationAndReimport(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	setting := `{"client_identity_mode":"per_account"}`
	a := model.Channel{Key: "old-credential", Setting: &setting, Status: 1}
	b := model.Channel{Key: "old-credential", Setting: &setting, Status: 1}
	require.NoError(t, model.DB.Create(&a).Error)
	require.NoError(t, model.DB.Create(&b).Error)
	require.Equal(t, "old-credential", a.Key)
	var stored string
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", a.Id).Pluck("key", &stored).Error)
	require.Contains(t, stored, "rillven-vault:")
	require.NotContains(t, stored, "old-credential")
	require.NoError(t, model.ApplyCodexCredentialCAS(t.Context(), a.Id, "old-credential", "new-credential", 1))
	loaded, err := model.GetChannelById(b.Id, true)
	require.NoError(t, err)
	require.Equal(t, "new-credential", loaded.Key)
	require.Error(t, model.ApplyCodexCredentialCAS(t.Context(), a.Id, "old-credential", "late-credential", 1))
	c := model.Channel{Key: "old-credential", Setting: &setting, Status: 1}
	require.NoError(t, model.DB.Create(&c).Error)
	require.Equal(t, "new-credential", c.Key)
	forged := model.Channel{Key: stored, Setting: &setting, Status: 1}
	require.Error(t, model.DB.Create(&forged).Error)
}
