package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// This final-egress check also protects channels with managed recovery disabled.
// Unknown upstream response IDs cannot be used as an authorization shortcut.
func ValidateAccountRequestHistory(c *gin.Context, profile *AttemptClientProfile, body []byte) (validationErr error) {
	defer func() {
		if validationErr != nil {
			validationErr = conversationAPIError(validationErr)
		}
	}()
	if profile == nil || profile.Mode != "per_account" || profile.Provider != "codex" {
		return nil
	}
	owner := ConversationOwner(c)
	if turn := GetConversationTurn(c); turn != nil {
		if err := model.CheckConversationDeletion(c.Request.Context(), turn.Owner, turn.SessionID); err != nil {
			return err
		}
		if turn.LookupError != nil {
			return turn.LookupError
		}
		if turn.UnknownState {
			return model.ErrConversationMissing
		}
	}
	if id := gjson.GetBytes(body, "previous_response_id").String(); id != "" {
		checkpoint, err := model.FindConversationAlias(c.Request.Context(), owner, conversationHash([]byte(id)))
		if err != nil {
			return err
		}
		if checkpoint.AccountID != profile.AccountID {
			stored := GetConversationTurn(c)
			if checkpoint.AccountID != "" || stored == nil || stored.Attempt == nil || !stored.Attempt.legacyNative || stored.Parent == nil || stored.Parent.ID != checkpoint.ID {
				return model.ErrConversationMissing
			}
		}
	}
	input, err := conversationInput([]byte(gjson.GetBytes(body, "input").Raw))
	if err != nil {
		return model.ErrConversationMissing
	}
	turn := &ConversationTurn{Owner: owner, Input: input, Original: body}
	if stored := GetConversationTurn(c); stored != nil {
		turn.Parent = stored.Parent
	}
	projected, err := projectAccountHistory(c.Request.Context(), turn, profile.AccountID, false)
	if err != nil {
		common.SysLog("account history projection rejected: " + err.Error())
		return err
	}
	for i, item := range input {
		if gjson.GetBytes(item, "type").String() == "item_reference" && string(projected[i]) != string(item) {
			return model.ErrConversationMissing
		}
	}
	for _, header := range []string{"X-Codex-Turn-State", "X-Codex-Routing-Hint"} {
		if raw := c.Request.Header.Get(header); raw != "" {
			checkpoint, err := model.FindConversationResource(c.Request.Context(), owner, "affinity", conversationHash([]byte(raw)))
			if err != nil {
				return err
			}
			if checkpoint.AccountID != profile.AccountID {
				return model.ErrConversationMissing
			}
		}
	}
	return nil
}
