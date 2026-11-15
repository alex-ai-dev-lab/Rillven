package service

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// Only a global default is advisory. Explicit model/channel endpoint bindings
// remain constraints even when a native Responses endpoint could preserve tools.
func CanUseNativeResponsesProtocolFallback(channel *model.Channel, route model.ModelRouteDecision) bool {
	return channel != nil && route.Source == model.ModelRouteSourceGlobal && route.Normalized &&
		route.Endpoint != constant.EndpointTypeOpenAIResponse && ChannelSupportsOpenAIResponses(channel)
}
