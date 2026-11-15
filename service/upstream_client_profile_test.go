package service

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupIdentityStateTest(t *testing.T) {
	t.Helper()
	keys, err := common.Marshal(map[string]string{"test": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	require.NoError(t, err)
	t.Setenv("RENEWAPI_STATE_KEYS", string(keys))
	t.Setenv("RENEWAPI_STATE_ACTIVE_KEY", "test")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "identity.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous; _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.ChannelIdentity{}, &model.ChannelCredentialSlot{}))
}

func TestAttemptClientProfileStableScopedIdentity(t *testing.T) {
	setupIdentityStateTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("Session-Id", "client-session")
	info := &relaycommon.RelayInfo{UserId: 1, TokenId: 2, TokenKey: "caller-key", RequestId: "r1", ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId: 1, ChannelType: constant.ChannelTypeOpenAI, ApiKey: "upstream-key",
		ChannelSetting: dto.ChannelSettings{ClientIdentityMode: "per_channel"},
	}}
	resolve := func() *AttemptClientProfile {
		profile, err := ResolveAttemptClientProfile(c, info, "/v1/responses", []byte(`{}`), false)
		require.NoError(t, err)
		return profile
	}
	a := resolve()
	info.ChannelMultiKeyIndex = 9
	again := resolve()
	require.Equal(t, a, again)
	info.TokenId++
	otherCaller := resolve()
	require.Equal(t, a.InstallationID, otherCaller.InstallationID)
	require.NotEqual(t, a.SessionID, otherCaller.SessionID)
	info.TokenId--
	info.ChannelId = 2
	b := resolve()
	require.NotEqual(t, a.InstallationID, b.InstallationID)
	require.NotEqual(t, a.SessionID, b.SessionID)
	info.ChannelId = 1
	info.ApiKey = "other-upstream-key"
	otherKey := resolve()
	require.NotEqual(t, a.SlotID, otherKey.SlotID)
	require.NotEqual(t, a.InstallationID, otherKey.InstallationID)
	body, headers, err := ApplyClientIdentityToRequestBytesWithOptions("/v1/responses", c.Request.Header, []byte(`{"input":"hello"}`), ClientIdentityOptions{Profile: a})
	require.NoError(t, err)
	require.Equal(t, a.InstallationID, gjson.GetBytes(body, `client_metadata.x-codex-installation-id`).String())
	require.Equal(t, a.SessionID, headers.Get("Session-Id"))
	require.Equal(t, "client-session", c.Request.Header.Get("Session-Id"), "attempts must not rewrite the original caller headers")
	info.ApiKey = "upstream-key"
	row, err := model.EnsureChannelIdentity(context.Background(), 1)
	require.NoError(t, err)
	require.NoError(t, model.UpdateChannelIdentity(context.Background(), 1, row.Revision, "", "", true))
	reset := resolve()
	require.Equal(t, a.SlotID, reset.SlotID)
	require.NotEqual(t, a.InstallationID, reset.InstallationID)
	require.NotEqual(t, a.SessionID, reset.SessionID)
	require.Error(t, model.UpdateChannelIdentity(context.Background(), 1, row.Revision, "", "", true), "stale revisions cannot reset a newer identity")
}

func TestAttemptClientProfileOAuthRotationKeepsIdentity(t *testing.T) {
	setupIdentityStateTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info := &relaycommon.RelayInfo{RequestId: "r1", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, ChannelType: constant.ChannelTypeCodex,
		ApiKey: `{"account_id":"account-a","email":"a@example.com","access_token":"old","refresh_token":"old"}`, ChannelSetting: dto.ChannelSettings{ClientIdentityMode: "per_channel"}}}
	before, err := ResolveAttemptClientProfile(c, info, "/v1/responses", []byte(`{}`), true)
	require.NoError(t, err)
	info.ApiKey = `{"account_id":"account-a","email":"a@example.com","access_token":"new","refresh_token":"new"}`
	after, err := ResolveAttemptClientProfile(c, info, "/v1/responses", []byte(`{}`), true)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestChannelIdentityConcurrentProvisioningAndExplicitConflicts(t *testing.T) {
	setupIdentityStateTest(t)
	var wg sync.WaitGroup
	rows := make([]*model.ChannelIdentity, 12)
	errs := make([]error, len(rows))
	for i := range rows {
		wg.Add(1)
		go func(i int) { defer wg.Done(); rows[i], errs[i] = model.EnsureChannelIdentity(context.Background(), 1) }(i)
	}
	wg.Wait()
	for i := range rows {
		require.NoError(t, errs[i])
		require.Equal(t, rows[0], rows[i])
	}
	second, err := model.EnsureChannelIdentity(context.Background(), 2)
	require.NoError(t, err)
	const installation = "11111111-1111-4111-8111-111111111111"
	require.NoError(t, model.UpdateChannelIdentity(context.Background(), 1, 1, installation, "device-a", false))
	require.Error(t, model.UpdateChannelIdentity(context.Background(), 2, second.Revision, installation, "device-b", false))
	reloaded, err := model.EnsureChannelIdentity(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, second.Revision, reloaded.Revision, "conflict must not partially update the seed/revision")
}
