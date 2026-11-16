package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const credentialReferencePrefix = "rillven-vault:"
const credentialPoolReferencePrefix = "rillven-vault-pool:"

func isCredentialReference(raw string) bool {
	return strings.HasPrefix(raw, credentialReferencePrefix) || strings.HasPrefix(raw, credentialPoolReferencePrefix)
}

type UpstreamCredential struct {
	ID         string    `gorm:"size:36;primaryKey" json:"id"`
	Version    int64     `gorm:"not null" json:"version"`
	Ciphertext StateBlob `gorm:"not null" json:"-"`
}

// Aliases are retained across rotation: importing an old copy cannot restore a
// consumed refresh token. Channels persist a reference, never token material.
type UpstreamCredentialAlias struct {
	Digest       string `gorm:"size:64;primaryKey"`
	CredentialID string `gorm:"size:36;not null;index"`
}

func MigrateUpstreamCredentials() error {
	return DB.AutoMigrate(&UpstreamCredential{}, &UpstreamCredentialAlias{})
}
func credentialDigest(raw string) string {
	var oauth struct {
		AccountID    string `json:"account_id"`
		RefreshToken string `json:"refresh_token"`
	}
	if common.Unmarshal([]byte(raw), &oauth) == nil && oauth.AccountID != "" && oauth.RefreshToken != "" {
		canonical, _ := common.Marshal([]string{"codex-grant", oauth.AccountID, oauth.RefreshToken})
		raw = string(canonical)
	}
	sum := sha256.Sum256([]byte("rillven-credential/v1/" + raw))
	return hex.EncodeToString(sum[:])
}
func credentialAAD(id string, version int64) string {
	return fmt.Sprintf("upstream-credential/v1/%s/%d", id, version)
}

// Rotation aliases retain an opaque credential's stable grant identity. A
// separately issued key needs an explicit administrator subject to join it.
func StableCredentialSubject(ctx context.Context, raw string) (string, error) {
	var alias UpstreamCredentialAlias
	err := stateDB(ctx).First(&alias, "digest = ?", credentialDigest(raw)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var ref string
		err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
			var err error
			ref, err = storeCredential(tx, raw)
			return err
		})
		return "credential/" + strings.TrimPrefix(ref, credentialReferencePrefix), err
	}
	if err != nil {
		return "", err
	}
	return "credential/" + alias.CredentialID, nil
}

// Encrypt inherited account-mode channels on first use without changing their
// configuration revision, so an in-flight route remains valid after backfill.
func LoadProtectedAccountChannel(ctx context.Context, id int) (*Channel, error) {
	var channel *Channel
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		var err error
		channel, err = GetChannelByIDTx(lockForUpdate(tx), id)
		if err != nil {
			return err
		}
		if !ChannelUsesAccountIdentity(channel) || channel.credentialReference != "" {
			return nil
		}
		key, err := channel.protectCredential(tx)
		if err != nil {
			return err
		}
		result := tx.Session(&gorm.Session{Logger: logger.Discard}).Model(&Channel{}).Where("id = ? AND config_version = ?", id, channel.ConfigVersion).Where(clause.Eq{Column: "key", Value: channel.Key}).Update("key", key)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrChannelConfigConflict
		}
		channel.credentialReference = key
		return nil
	})
	return channel, err
}
func readCredential(tx *gorm.DB, id string) (*UpstreamCredential, string, error) {
	var row UpstreamCredential
	if err := lockForUpdate(tx.Session(&gorm.Session{NewDB: true, Logger: logger.Discard})).First(&row, "id = ?", id).Error; err != nil {
		return nil, "", err
	}
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return nil, "", err
	}
	plain, err := cipher.Open(credentialAAD(row.ID, row.Version), string(row.Ciphertext))
	if err != nil {
		return nil, "", errors.New("upstream credential cannot be decrypted")
	}
	return &row, string(plain), nil
}

func storeCredential(tx *gorm.DB, raw string) (string, error) {
	if isCredentialReference(raw) {
		return "", errors.New("credential references cannot be supplied as credentials")
	}
	digest := credentialDigest(raw)
	var alias UpstreamCredentialAlias
	db := tx.Session(&gorm.Session{NewDB: true, Logger: logger.Discard})
	err := db.First(&alias, "digest = ?", digest).Error
	if err == nil {
		return credentialReferencePrefix + alias.CredentialID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return "", err
	}
	row := UpstreamCredential{ID: uuid.NewString(), Version: 1}
	sealed, err := cipher.Seal(credentialAAD(row.ID, row.Version), []byte(raw))
	if err != nil {
		return "", err
	}
	row.Ciphertext = StateBlob(sealed)
	if err := db.Create(&row).Error; err != nil {
		return "", err
	}
	alias = UpstreamCredentialAlias{Digest: digest, CredentialID: row.ID}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&alias).Error; err != nil {
		return "", err
	}
	alias = UpstreamCredentialAlias{}
	if err := lockForUpdate(db).First(&alias, "digest = ?", digest).Error; err != nil {
		return "", err
	}
	if alias.CredentialID != row.ID {
		if err := db.Delete(&row).Error; err != nil {
			return "", err
		}
	}
	return credentialReferencePrefix + alias.CredentialID, nil
}

func (channel *Channel) protectCredential(tx *gorm.DB) (string, error) {
	if channel.Key == "" {
		return "", nil
	}
	if isCredentialReference(channel.Key) {
		if channel.credentialReference != channel.Key {
			return "", errors.New("untrusted credential reference")
		}
		return channel.Key, nil
	}
	if !ChannelUsesAccountIdentity(channel) && channel.credentialReference == "" {
		return channel.Key, nil
	}
	if !channel.ChannelInfo.IsMultiKey {
		return storeCredential(tx, channel.Key)
	}
	keys := (&Channel{Key: channel.Key}).GetKeys()
	refs := make([]string, 0, len(keys))
	for _, key := range keys {
		ref, err := storeCredential(tx, key)
		if err != nil {
			return "", err
		}
		refs = append(refs, ref)
	}
	encoded, err := common.Marshal(refs)
	return credentialPoolReferencePrefix + string(encoded), err
}

func (channel *Channel) BeforeCreate(tx *gorm.DB) error {
	key, err := channel.protectCredential(tx)
	if err != nil {
		return err
	}
	if isCredentialReference(key) {
		channel.credentialReference = key
		channel.Key = key
	}
	return nil
}
func (channel *Channel) AfterCreate(tx *gorm.DB) error { return channel.AfterFind(tx) }
func (channel *Channel) BeforeUpdate(tx *gorm.DB) error {
	// Struct saves must preserve encryption; map updates use the explicit config API.
	if _, ok := tx.Statement.Dest.(*Channel); !ok {
		return nil
	}
	columns, restricted := tx.Statement.SelectAndOmitColumns(false, false)
	if included, exists := columns["key"]; exists && !included || restricted && !included {
		return nil
	}
	key, err := channel.protectCredential(tx)
	if err != nil {
		return err
	}
	if isCredentialReference(key) {
		channel.credentialReference = key
		channel.Key = key
	}
	return nil
}
func (channel *Channel) AfterUpdate(tx *gorm.DB) error {
	if _, ok := tx.Statement.Dest.(*Channel); ok && isCredentialReference(channel.Key) {
		return channel.AfterFind(tx)
	}
	return nil
}
func (channel *Channel) AfterFind(tx *gorm.DB) error {
	if !isCredentialReference(channel.Key) {
		channel.credentialReference = ""
		return nil
	}
	ref := channel.Key
	if strings.HasPrefix(ref, credentialPoolReferencePrefix) {
		var refs []string
		if common.Unmarshal([]byte(strings.TrimPrefix(ref, credentialPoolReferencePrefix)), &refs) != nil || len(refs) == 0 {
			return errors.New("invalid credential pool reference")
		}
		keys := make([]common.RawMessage, 0, len(refs))
		for _, item := range refs {
			if !strings.HasPrefix(item, credentialReferencePrefix) {
				return errors.New("invalid credential slot reference")
			}
			_, raw, err := readCredential(tx, strings.TrimPrefix(item, credentialReferencePrefix))
			if err != nil {
				return err
			}
			var value any
			encoded := []byte(raw)
			if common.Unmarshal(encoded, &value) != nil {
				encoded, err = common.Marshal(raw)
				if err != nil {
					return err
				}
			}
			keys = append(keys, common.RawMessage(encoded))
		}
		encoded, err := common.Marshal(keys)
		if err != nil {
			return err
		}
		channel.Key, channel.credentialReference, channel.Keys = string(encoded), ref, nil
		return nil
	}
	_, raw, err := readCredential(tx, strings.TrimPrefix(ref, credentialReferencePrefix))
	if err != nil {
		return err
	}
	channel.Key, channel.credentialReference = raw, ref
	channel.Keys = nil
	return nil
}

// A row lock and version CAS protect the shared fact, so channel copies observe
// the same successful rotation even if the initiating request is cancelled.
func applyVaultCredentialCAS(ctx context.Context, ch *Channel, prior, next string, configVersion ...int64) error {
	return retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		fresh, err := GetChannelByIDTx(lockForUpdate(tx), ch.Id)
		if err != nil {
			return err
		}
		if fresh.credentialReference != ch.credentialReference || fresh.Status != common.ChannelStatusEnabled && fresh.Status != common.ChannelStatusAutoDisabled {
			return errors.New("channel changed while refreshing")
		}
		if len(configVersion) > 0 && fresh.ConfigVersion != configVersion[0] {
			return ErrChannelConfigConflict
		}
		id := strings.TrimPrefix(fresh.credentialReference, credentialReferencePrefix)
		if strings.HasPrefix(fresh.credentialReference, credentialPoolReferencePrefix) {
			var refs []string
			if common.Unmarshal([]byte(strings.TrimPrefix(fresh.credentialReference, credentialPoolReferencePrefix)), &refs) != nil {
				return errors.New("invalid credential pool reference")
			}
			keys := fresh.GetKeys()
			id = ""
			for index, key := range keys {
				if key == prior || key == next {
					if status, exists := fresh.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled && status != common.ChannelStatusAutoDisabled {
						return errors.New("credential disabled while refreshing")
					}
					id = strings.TrimPrefix(refs[index], credentialReferencePrefix)
					break
				}
			}
			if id == "" {
				return errors.New("credential slot changed while refreshing")
			}
		}
		var locked UpstreamCredential
		if err := lockForUpdate(tx).First(&locked, "id = ?", id).Error; err != nil {
			return err
		}
		row, raw, err := readCredential(tx, id)
		if err != nil {
			return err
		}
		if raw == next {
			return nil
		}
		if raw != prior {
			return errors.New("credential version changed while refreshing")
		}
		cipher, err := common.RelayStateCipher()
		if err != nil {
			return err
		}
		sealed, err := cipher.Seal(credentialAAD(id, row.Version+1), []byte(next))
		if err != nil {
			return err
		}
		result := tx.Model(&UpstreamCredential{}).Where("id = ? AND version = ?", id, row.Version).Updates(map[string]any{"version": row.Version + 1, "ciphertext": StateBlob(sealed)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("credential rotation conflict")
		}
		alias := UpstreamCredentialAlias{Digest: credentialDigest(next), CredentialID: id}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&alias).Error; err != nil {
			return err
		}
		var saved UpstreamCredentialAlias
		if err := tx.First(&saved, "digest = ?", alias.Digest).Error; err != nil {
			return err
		}
		if saved.CredentialID != id {
			return errors.New("rotated credential is bound to a different record")
		}
		return nil
	})
}

func SetChannelCredential(ctx context.Context, id int, raw string) error {
	return retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		ch, err := GetChannelByIDTx(tx, id)
		if err != nil {
			return err
		}
		ch.Key = raw
		key, err := ch.protectCredential(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&Channel{}).Where("id = ? AND config_version = ?", id, ch.ConfigVersion).Updates(map[string]any{"key": key, "config_version": gorm.Expr("config_version + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrChannelConfigConflict
		}
		return nil
	})
}
