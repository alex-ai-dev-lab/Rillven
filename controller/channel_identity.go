package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func channelForIdentity(c *gin.Context) (*model.Channel, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid channel ID"})
		return nil, false
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil || channel == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Channel not found"})
		return nil, false
	}
	return channel, true
}

// Explicit administrator preview provisions stable identities without contacting
// upstreams. Neither credentials, seeds nor encryption keys are returned.
func PreviewChannelIdentity(c *gin.Context) {
	channel, ok := channelForIdentity(c)
	if !ok {
		return
	}
	row, err := model.EnsureChannelIdentity(c.Request.Context(), channel.Id)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": err.Error()})
		return
	}
	secret, err := row.Secret()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Channel identity cannot be decrypted"})
		return
	}
	setting := channel.GetSetting()
	setting.ClientIdentityMode = "per_channel"
	path := "/v1/responses"
	if channel.Type == 14 {
		path = "/v1/messages"
	}
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
	}
	previews := make([]gin.H, 0, min(len(keys), 20))
	for i, key := range keys {
		if i >= 20 {
			break
		}
		info := &relaycommon.RelayInfo{RequestId: "identity-preview", ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channel.Id, ChannelType: channel.Type, ApiKey: key, ChannelSetting: setting, ChannelIsMultiKey: channel.ChannelInfo.IsMultiKey,
		}}
		profile, err := service.ResolveAttemptClientProfile(c, info, path, []byte(`{}`), false)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
		previews = append(previews, gin.H{"slot_id": profile.SlotID, "installation_id": profile.InstallationID, "device_id": profile.DeviceID, "headers": profile.Headers})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"channel_id": channel.Id, "revision": row.Revision, "installation_id": secret.InstallationID,
		"device_id": secret.DeviceID, "slots": previews, "multi_key": channel.ChannelInfo.IsMultiKey,
	}})
}

func UpdateChannelIdentity(c *gin.Context) {
	channel, ok := channelForIdentity(c)
	if !ok {
		return
	}
	var request struct {
		Revision       int64  `json:"revision"`
		InstallationID string `json:"installation_id"`
		DeviceID       string `json:"device_id"`
		Reset          bool   `json:"reset"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Revision <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid identity update"})
		return
	}
	if channel.ChannelInfo.IsMultiKey && (request.InstallationID != "" || request.DeviceID != "") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Multi-key channels use independent credential identities; split channels to set explicit IDs"})
		return
	}
	if err := model.UpdateChannelIdentity(c.Request.Context(), channel.Id, request.Revision, request.InstallationID, request.DeviceID, request.Reset); err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Identity update failed (invalid ID, duplicate ID or stale revision). Reload and check the values."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
