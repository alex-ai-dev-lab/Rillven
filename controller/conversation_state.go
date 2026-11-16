package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ListOwnedConversations(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	rows, err := model.ListOwnedConversationSessions(c.Request.Context(), service.ConversationOwner(c), c.Query("before"), limit)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conversation storage unavailable"})
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"id": row.ID, "created_at": row.CreatedAt, "expires_at": row.ExpiresAt, "bytes": row.Bytes})
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func DeleteOwnedConversation(c *gin.Context) {
	err := model.DeleteOwnedConversationSession(c.Request.Context(), service.ConversationOwner(c), c.Param("id"))
	if err != nil {
		status, message := http.StatusServiceUnavailable, "conversation storage unavailable"
		if errors.Is(err, model.ErrConversationMissing) {
			status, message = http.StatusNotFound, "conversation unavailable"
		}
		if errors.Is(err, model.ErrConversationBusy) {
			status, message = http.StatusAccepted, "conversation access is blocked; content cleanup is waiting for the active or unsettled turn"
		}
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.Status(http.StatusNoContent)
}

func DeleteAllOwnedConversations(c *gin.Context) {
	count, err := model.DeleteOwnedConversationSessions(c.Request.Context(), service.ConversationOwner(c), 128)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conversation storage unavailable"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"requested": count, "repeat_until_zero": true})
}

func ExportConversationDeletions(c *gin.Context) {
	rows, err := model.ListConversationDeletions(c.Request.Context(), c.Query("after"), 256)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conversation storage unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func ImportConversationDeletions(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, (128<<10)+1))
	var request struct {
		Data []model.ConversationDeletion `json:"data"`
	}
	if err != nil || len(body) > 128<<10 || common.Unmarshal(body, &request) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid deletion tombstone batch"})
		return
	}
	if err := model.ImportConversationDeletions(c.Request.Context(), request.Data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deletion tombstone import failed; keep restored traffic disabled"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": len(request.Data)})
}

func ReencryptRelayState(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "128"))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	count, err := service.ReencryptRelayState(ctx, limit)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "updated": count, "message": "State re-encryption failed; keep all old keys and retry"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "updated": count, "repeat_until_zero": true})
}
