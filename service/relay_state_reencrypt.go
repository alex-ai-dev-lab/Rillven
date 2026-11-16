package service

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func ReencryptRelayState(ctx context.Context, limit int) (int, error) {
	limit = max(1, min(limit, 256))
	count, err := model.ReencryptAccountState(ctx, limit)
	if err != nil {
		return count, err
	}
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return count, err
	}
	var checkpoints []model.ConversationCheckpoint
	if err := model.DB.WithContext(ctx).Where("ciphertext NOT LIKE ?", "v1."+cipher.ActiveKeyID()+".%").Limit(limit).Find(&checkpoints).Error; err != nil {
		return count, err
	}
	for _, checkpoint := range checkpoints {
		next, err := cipher.Reseal(conversationCheckpointAAD(&checkpoint), string(checkpoint.Ciphertext))
		if err != nil {
			return count, err
		}
		changed, err := model.ReplaceCheckpointCiphertext(ctx, checkpoint.ID, checkpoint.Owner, checkpoint.Ciphertext, model.StateBlob(next))
		if err != nil {
			return count, err
		}
		if changed {
			count++
		}
	}
	return count, nil
}
