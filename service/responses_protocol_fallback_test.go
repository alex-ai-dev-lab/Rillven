package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/openaicompat"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestResponsesToolsUseNativeFallbackWithoutDroppingFeatures(t *testing.T) {
	previous := operation_setting.ModelEndpointDefaults2JsonString()
	require.NoError(t, operation_setting.UpdateModelEndpointDefaultsByJsonString(`{"enabled":true,"normalize_text_endpoints":true,"entries":[{"match_type":"prefix","pattern":"codex","channel_type":1,"default_endpoint":"openai","auto_correct":true}]}`))
	t.Cleanup(func() { require.NoError(t, operation_setting.UpdateModelEndpointDefaultsByJsonString(previous)) })
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI}
	for _, tools := range []string{
		`[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}]`,
		`[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"lookup","defer_loading":true}]}]`,
	} {
		req := &dto.OpenAIResponsesRequest{Model: "codex-auto-review", Input: common.RawMessage(`"hello"`), Tools: common.RawMessage(tools)}
		err := ValidateNormalizedBridgeRequest(req, constant.EndpointTypeOpenAI)
		require.True(t, openaicompat.IsUnsupportedProtocolConversion(err), "%v", err)
		capability := EvaluateChannelProtocolCapability(channel, req.Model, types.RelayFormatOpenAIResponses, req)
		require.True(t, capability.Supported, capability.Reason)
		require.Equal(t, constant.EndpointTypeOpenAIResponse, capability.Endpoint)
		require.Equal(t, tools, string(req.Tools))
	}
	// Unsupported first declaration must not conceal a malformed later one.
	req := &dto.OpenAIResponsesRequest{Tools: common.RawMessage(`[{"type":"custom","name":"a"},{"type":"namespace","name":"n","tools":[42]}]`)}
	err := ValidateNormalizedBridgeRequest(req, constant.EndpointTypeOpenAI)
	require.Error(t, err)
	require.False(t, openaicompat.IsUnsupportedProtocolConversion(err))
	require.False(t, CanUseNativeResponsesProtocolFallback(channel, model.ModelRouteDecision{Source: model.ModelRouteSourceNative, Normalized: true, Endpoint: constant.EndpointTypeOpenAI}))
}
