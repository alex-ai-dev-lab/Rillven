package relay

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func useNativeResponsesProtocolFallback(c *gin.Context, info *relaycommon.RelayInfo) bool {
	channel, err := model.CacheGetChannel(info.ChannelId)
	if err != nil || !service.CanUseNativeResponsesProtocolFallback(channel, model.ModelRouteDecision{
		Source: model.ModelRouteSource(info.RouteSource), Normalized: info.ProtocolNormalized, Endpoint: info.RouteEndpoint,
	}) {
		return false
	}
	apiType, ok := common.ChannelType2APIType(channel.Type)
	if !ok {
		return false
	}
	info.ChannelType, info.ApiType = channel.Type, apiType
	info.RouteEndpoint, info.RouteSource = constant.EndpointTypeOpenAIResponse, string(model.ModelRouteSourceNative)
	info.RouteOverridden, info.ProtocolNormalized = false, false
	common.SetContextKey(c, constant.ContextKeyChannelType, info.ChannelType)
	common.SetContextKey(c, constant.ContextKeyChannelRouteEndpoint, string(info.RouteEndpoint))
	common.SetContextKey(c, constant.ContextKeyChannelRouteSource, info.RouteSource)
	common.SetContextKey(c, constant.ContextKeyChannelRouteOverridden, false)
	common.SetContextKey(c, constant.ContextKeyProtocolNormalized, false)
	service.RecordRequestNormalization(info, "used native Responses to preserve source features")
	c.Header("X-NewAPI-Endpoint-Corrected", "")
	return true
}
