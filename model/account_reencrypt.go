package model

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Re-encryption changes no identity/grant revisions or AAD. A ciphertext CAS
// prevents the scan from overwriting a concurrently refreshed/reset record.
func ReencryptAccountState(ctx context.Context, limit int) (int, error) {
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return 0, err
	}
	prefix := "v1." + cipher.ActiveKeyID() + ".%"
	db := stateDB(ctx)
	count := 0
	update := func(table any, where string, id any, prior, aad string) error {
		if strings.HasPrefix(prior, "v1."+cipher.ActiveKeyID()+".") {
			return nil
		}
		next, err := cipher.Reseal(aad, prior)
		if err != nil {
			return err
		}
		result := db.Model(table).Where(where, id).Where("ciphertext = ?", StateBlob(prior)).Update("ciphertext", StateBlob(next))
		if result.Error != nil {
			return result.Error
		}
		count += int(result.RowsAffected)
		return nil
	}
	var channels []ChannelIdentity
	if err := db.Where("ciphertext NOT LIKE ?", prefix).Limit(limit).Find(&channels).Error; err != nil {
		return count, err
	}
	for _, row := range channels {
		if err := update(&ChannelIdentity{}, "channel_id = ?", row.ChannelID, row.Ciphertext, row.aad()); err != nil {
			return count, err
		}
	}
	var accounts []UpstreamAccount
	if err := db.Where("ciphertext NOT LIKE ?", prefix).Limit(limit).Find(&accounts).Error; err != nil {
		return count, err
	}
	for _, row := range accounts {
		if err := update(&UpstreamAccount{}, "id = ?", row.ID, row.Ciphertext, row.aad()); err != nil {
			return count, err
		}
	}
	var credentials []UpstreamCredential
	if err := db.Where("ciphertext NOT LIKE ?", prefix).Limit(limit).Find(&credentials).Error; err != nil {
		return count, err
	}
	for _, row := range credentials {
		if err := update(&UpstreamCredential{}, "id = ?", row.ID, string(row.Ciphertext), credentialAAD(row.ID, row.Version)); err != nil {
			return count, err
		}
	}
	var grants []CodexRefreshJournal
	if err := db.Where("state = ? AND ciphertext NOT LIKE ?", "received", prefix).Limit(limit).Find(&grants).Error; err != nil {
		return count, err
	}
	for _, row := range grants {
		if err := update(&CodexRefreshJournal{}, "grant_hash = ?", row.GrantHash, string(row.Ciphertext), "codex-refresh/"+row.GrantHash); err != nil {
			return count, err
		}
	}
	return count, nil
}

func ReplaceCheckpointCiphertext(ctx context.Context, id, owner string, prior, next StateBlob) (bool, error) {
	result := stateDB(ctx).Model(&ConversationCheckpoint{}).Where("id = ? AND owner = ? AND ciphertext = ?", id, owner, prior).Update("ciphertext", next)
	return result.RowsAffected == 1, result.Error
}
