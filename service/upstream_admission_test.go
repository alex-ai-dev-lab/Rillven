package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUpstreamAdmissionSharesQuotaAcrossChannelCopies(t *testing.T) {
	setupIdentityStateTest(t)
	require.NoError(t, model.MigrateUpstreamAdmission())
	settings := dto.ChannelSettings{UpstreamCoordination: true}
	info := &relaycommon.RelayInfo{OriginModelName: "model", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1, ChannelType: 1, ChannelBaseUrl: "https://upstream.example", ApiKey: "shared-key", ChannelSetting: settings}}
	ObserveUpstreamQuotaHeaders(context.Background(), info, http.Header{"X-Ratelimit-Remaining-Requests": {"0"}, "X-Ratelimit-Reset-Requests": {"1h"}})
	info.ChannelId = 2
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, settings)
	err := WithUpstreamAdmission(c, info, func() *types.NewAPIError { t.Fatal("same account must respect saved quota"); return nil })
	require.NotNil(t, err)
	require.True(t, types.IsGatewayCoordinationError(err))
	require.False(t, IsChannelFailureError(err))
	require.False(t, ShouldRouterCooldownTrackError(err))
	require.False(t, ShouldCountChannelFailureForDisable(err))
	info.ApiKey = "different-key"
	calls := 0
	err = WithUpstreamAdmission(c, info, func() *types.NewAPIError { calls++; return nil })
	require.Nil(t, err)
	require.Equal(t, 1, calls)
	var count int64
	require.NoError(t, model.DB.Model(&model.UpstreamAdmission{}).Count(&count).Error)
	require.Zero(t, count)
}
