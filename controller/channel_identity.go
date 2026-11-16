package controller

import (
	"github.com/google/uuid"
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
	row, err := model.EnsureChannelIdentity(c.Request.Context(), channel.Id, model.ChannelUsesAccountIdentity(channel))
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
	if model.ChannelUsesAccountIdentity(channel) {
		setting.ClientIdentityMode = "per_account"
	} else {
		setting.ClientIdentityMode = "per_channel"
	}
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
		info := &relaycommon.RelayInfo{RequestId: "identity-preview", ClientProfilePreview: true, ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channel.Id, ChannelType: channel.Type, ApiKey: key, ChannelSetting: setting, ChannelIsMultiKey: channel.ChannelInfo.IsMultiKey,
		}}
		profile, err := service.ResolveAttemptClientProfile(c, info, path, []byte(`{}`), false)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
		previews = append(previews, gin.H{"slot_id": profile.SlotID, "installation_id": profile.InstallationID, "device_id": profile.DeviceID, "headers": profile.Headers, "account_id": profile.AccountID, "revision": profile.AccountRevision, "credential_index": i})
	}
	revision, installation, device := row.Revision, secret.InstallationID, secret.DeviceID
	if setting.ClientIdentityMode == "per_account" && len(previews) > 0 {
		revision = previews[0]["revision"].(int64)
		installation = previews[0]["installation_id"].(string)
		device = previews[0]["device_id"].(string)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"channel_id": channel.Id, "revision": revision, "installation_id": installation,
		"device_id": device, "slots": previews, "multi_key": channel.ChannelInfo.IsMultiKey, "mode": setting.ClientIdentityMode,
	}})
}

func UpdateChannelIdentity(c *gin.Context) {
	channel, ok := channelForIdentity(c)
	if !ok {
		return
	}
	var request struct {
		Revision        int64  `json:"revision"`
		InstallationID  string `json:"installation_id"`
		DeviceID        string `json:"device_id"`
		Reset           bool   `json:"reset"`
		AccountID       string `json:"account_id"`
		CredentialIndex int    `json:"credential_index"`
		UpdateProfile   bool   `json:"update_profile"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Revision <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid identity update"})
		return
	}
	if model.ChannelUsesAccountIdentity(channel) {
		keys := []string{channel.Key}
		if channel.ChannelInfo.IsMultiKey {
			keys = channel.GetKeys()
		}
		if request.CredentialIndex < 0 || request.CredentialIndex >= len(keys) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid credential slot"})
			return
		}
		setting := channel.GetSetting()
		setting.ClientIdentityMode = "per_account"
		if request.UpdateProfile {
			if err := setting.ValidateHTTPTransport(); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid channel client profile"})
				return
			}
			var bindings int64
			if err := model.DB.WithContext(c.Request.Context()).Model(&model.ChannelAccountBinding{}).Where("channel_id = ? AND account_id = ?", channel.Id, request.AccountID).Count(&bindings).Error; err != nil || bindings == 0 {
				c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Account binding changed; reload the identity"})
				return
			}
			version, userAgent, originator := service.AccountClientProfileValues(channel.Type, setting)
			if err := model.UpdateUpstreamAccountProfile(c.Request.Context(), request.AccountID, request.Revision, version, userAgent, originator); err != nil {
				c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Account profile update failed; reload the identity"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"success": true})
			return
		}
		info := &relaycommon.RelayInfo{RequestId: "identity-update", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ApiKey: keys[request.CredentialIndex], ChannelSetting: setting, ChannelIsMultiKey: channel.ChannelInfo.IsMultiKey}}
		path := "/v1/responses"
		if channel.Type == 14 {
			path = "/v1/messages"
		}
		profile, err := service.ResolveAttemptClientProfile(c, info, path, []byte(`{}`), false)
		if err != nil || request.AccountID != profile.AccountID {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Account binding changed; reload the identity"})
			return
		}
		if request.Reset {
			request.InstallationID = uuid.NewString()
		}
		if err := model.UpdateUpstreamAccountIdentity(c.Request.Context(), profile.AccountID, request.Revision, request.InstallationID, request.DeviceID); err != nil {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Account identity update failed; reload and check the values"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
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
