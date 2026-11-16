package service

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func claimOrReplayConversationRequest(c *gin.Context, turn *ConversationTurn, info *relaycommon.RelayInfo) *types.NewAPIError {
	key := c.GetHeader("Idempotency-Key")
	if key == "" || !turn.Managed || turn.Shadow {
		return nil
	}
	if len(key) > 256 {
		return types.NewErrorWithStatusCode(errors.New("Idempotency-Key exceeds 256 bytes"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	var fields any
	if common.UnmarshalPreserveNumbers(turn.Original, &fields) != nil {
		return types.NewConversationContextUnavailable()
	}
	canonical, err := common.Marshal(fields)
	if err != nil {
		return types.NewConversationStoreUnavailable()
	}
	id := conversationHash([]byte(turn.Owner + "/idempotency/" + key))
	hash := conversationHash(append([]byte(c.Request.URL.Path+"\x00"), canonical...))
	claim, created, err := model.ClaimConversationRequest(c.Request.Context(), turn.Owner, id, hash, ManagedResponsePrefix)
	if errors.Is(err, model.ErrConversationRequestConflict) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusConflict, types.ErrOptionWithSkipRetry())
	}
	if err != nil {
		return types.NewConversationStoreUnavailable()
	}
	if created {
		turn.RequestID, turn.ResponseID, info.RequestId = claim.ID, claim.ResponseID, claim.ID
		return nil
	}
	checkpoint, err := model.FindConversationCheckpoint(c.Request.Context(), turn.Owner, claim.ResponseID)
	if errors.Is(err, model.ErrConversationMissing) || errors.Is(err, model.ErrConversationBusy) {
		return types.NewErrorWithStatusCode(errors.New("This request is still pending, unavailable or has an unconfirmed result; it will not be generated again."), types.ErrorCodeConversationContextUnavailable, http.StatusConflict, types.ErrOptionWithSkipRetry())
	}
	if err != nil {
		return types.NewConversationStoreUnavailable()
	}
	payload, err := readCheckpointPayload(c.Request.Context(), turn.Owner, checkpoint)
	if err != nil || len(payload.Response) == 0 {
		return types.NewConversationStoreUnavailable()
	}
	c.Header("X-Rillven-Replayed", "true")
	SetRelaySemanticSuccess(c, true)
	if gjson.GetBytes(turn.Original, "stream").Bool() {
		kind := "response.completed"
		if gjson.GetBytes(payload.Response, "status").String() == "incomplete" {
			kind = "response.incomplete"
		}
		event, err := common.Marshal(map[string]any{"type": kind, "response": payload.Response})
		if err != nil {
			return types.NewConversationStoreUnavailable()
		}
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", event)
		c.Writer.Flush()
	} else {
		c.Data(http.StatusOK, "application/json", payload.Response)
	}
	c.Abort()
	return nil
}
