package model

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrAccountIdentityConflict = errors.New("upstream account identity conflicts with an existing binding; select the canonical account explicitly")

// SubjectHash identifies the authenticated upstream subject, never a channel,
// caller or rotating access token. UUID and secret travel with database backups.
type UpstreamAccount struct {
	ID               string  `gorm:"size:36;primaryKey" json:"id"`
	Provider         string  `gorm:"size:32;not null" json:"provider"`
	SubjectHash      string  `gorm:"size:64;not null;uniqueIndex" json:"-"`
	Revision         int64   `gorm:"not null" json:"revision"`
	IdentityRevision int64   `gorm:"not null;default:1" json:"identity_revision"`
	ProfileRevision  int64   `gorm:"not null;default:1" json:"profile_revision"`
	Ciphertext       string  `gorm:"type:text;not null" json:"-"`
	InstallationHash *string `gorm:"size:64;uniqueIndex" json:"-"`
	DeviceHash       *string `gorm:"size:64;uniqueIndex" json:"-"`
}

type AccountIdentitySecret struct {
	Seed            string `json:"seed"`
	NewInstallation bool   `json:"-"`
	ExplicitProfile bool   `json:"-"`
	// LegacySlot preserves the winning channel's installed identity projection.
	LegacySlot     string `json:"legacy_slot"`
	LegacyRevision int64  `json:"legacy_revision"`
	InstallationID string `json:"installation_id"`
	DeviceID       string `json:"device_id"`
	ClientVersion  string `json:"client_version"`
	UserAgent      string `json:"user_agent"`
	Originator     string `json:"originator"`
}

type ChannelAccountBinding struct {
	ChannelID int    `gorm:"primaryKey;autoIncrement:false" json:"channel_id"`
	SlotID    string `gorm:"size:36;primaryKey" json:"slot_id"`
	AccountID string `gorm:"size:36;not null;index" json:"account_id"`
}

func MigrateUpstreamAccounts() error {
	backfill := DB.Migrator().HasTable(&UpstreamAccount{}) && !DB.Migrator().HasColumn(&UpstreamAccount{}, "IdentityRevision")
	if err := DB.AutoMigrate(&UpstreamAccount{}, &ChannelAccountBinding{}, &AccountCorrelation{}, &AccountContextSequence{}, &AccountContextAllocation{}); err != nil {
		return err
	}
	if backfill {
		return DB.Model(&UpstreamAccount{}).Where("revision > 1").UpdateColumn("identity_revision", gorm.Expr("revision")).Error
	}
	return nil
}

func (a *UpstreamAccount) aad() string {
	return fmt.Sprintf("upstream-account/v1/%s/%s/%s/%d", a.ID, a.Provider, a.SubjectHash, a.Revision)
}

func (a *UpstreamAccount) Secret() (AccountIdentitySecret, error) {
	var secret AccountIdentitySecret
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return secret, err
	}
	raw, err := cipher.Open(a.aad(), a.Ciphertext)
	if err != nil {
		return secret, errors.New("upstream account identity cannot be decrypted")
	}
	if err := common.Unmarshal(raw, &secret); err != nil {
		return secret, err
	}
	seed, err := base64.StdEncoding.DecodeString(secret.Seed)
	if err != nil || len(seed) != 32 || secret.LegacySlot == "" || secret.InstallationID == "" {
		return secret, errors.New("invalid upstream account identity")
	}
	return secret, nil
}

func sealAccount(a *UpstreamAccount, secret AccountIdentitySecret) error {
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return err
	}
	raw, err := common.Marshal(secret)
	if err != nil {
		return err
	}
	a.Ciphertext, err = cipher.Seal(a.aad(), raw)
	a.InstallationHash, a.DeviceHash = optionalIdentityHash(secret.InstallationID), optionalIdentityHash(secret.DeviceID)
	return err
}

func AccountSubjectHash(provider, subject string) string {
	raw, _ := common.Marshal([]string{"upstream-account/v1", provider, subject})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// Provisioning re-reads the persisted winner after a unique-index race. Existing
// identities are never rotated by a new channel, caller or configuration form.
func EnsureUpstreamAccount(ctx context.Context, provider, subject string, initial AccountIdentitySecret, expectedID string, checkLegacy bool, preview ...bool) (*UpstreamAccount, error) {
	if provider == "" || subject == "" {
		return nil, errors.New("stable upstream subject is required")
	}
	hash := AccountSubjectHash(provider, subject)
	var row UpstreamAccount
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		row = UpstreamAccount{}
		err := tx.Where("subject_hash = ?", hash).First(&row).Error
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if expectedID != "" {
			return ErrAccountIdentityConflict
		}
		candidate := UpstreamAccount{ID: uuid.NewString(), Provider: provider, SubjectHash: hash, Revision: 1}
		secret := initial
		if secret.NewInstallation {
			secret.InstallationID = uuid.NewString()
		}
		if err := sealAccount(&candidate, secret); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return err
		}
		// MySQL repeatable-read snapshots can predate the concurrent winner.
		// A locking read sees the row whose insert our upsert waited for.
		return lockForUpdate(tx).Where("subject_hash = ?", hash).First(&row).Error
	})
	if err != nil {
		return nil, err
	}
	if expectedID != "" && expectedID != row.ID {
		return nil, ErrAccountIdentityConflict
	}
	secret, err := row.Secret()
	if err != nil {
		return nil, err
	}
	if checkLegacy && expectedID == "" && (secret.InstallationID != initial.InstallationID || secret.DeviceID != initial.DeviceID) {
		return nil, ErrAccountIdentityConflict
	}
	allowMismatch := len(preview) > 0 && preview[0]
	if !allowMismatch && initial.ClientVersion != "" && initial.ClientVersion != secret.ClientVersion {
		return nil, errors.New("client version conflicts with the pinned account profile")
	}
	if !allowMismatch && initial.ExplicitProfile && initial.UserAgent != secret.UserAgent {
		return nil, errors.New("client platform or user agent conflicts with the pinned account profile")
	}
	return &row, nil
}

func BindChannelAccount(ctx context.Context, channelID int, slotID, accountID string) error {
	return retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		row := ChannelAccountBinding{ChannelID: channelID, SlotID: slotID, AccountID: accountID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		var saved ChannelAccountBinding
		if err := tx.Where("channel_id = ? AND slot_id = ?", channelID, slotID).First(&saved).Error; err != nil {
			return err
		}
		if saved.AccountID != accountID {
			return ErrAccountIdentityConflict
		}
		return nil
	})
}

func UpdateUpstreamAccountIdentity(ctx context.Context, id string, revision int64, installationID, deviceID string) error {
	if installationID != "" {
		if _, err := uuid.Parse(installationID); err != nil {
			return errors.New("installation ID must be a UUID")
		}
	}
	if len(deviceID) > 128 {
		return errors.New("device ID exceeds 128 characters")
	}
	for _, ch := range deviceID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return errors.New("invalid device ID")
		}
	}
	var row UpstreamAccount
	if err := stateDB(ctx).First(&row, "id = ?", id).Error; err != nil {
		return err
	}
	if row.Revision != revision {
		return ErrAccountIdentityConflict
	}
	secret, err := row.Secret()
	if err != nil {
		return err
	}
	if installationID == "" {
		installationID = secret.InstallationID
	}
	secret.InstallationID, secret.DeviceID = installationID, deviceID
	row.Revision++
	row.IdentityRevision++
	if err := sealAccount(&row, secret); err != nil {
		return err
	}
	result := stateDB(ctx).Model(&UpstreamAccount{}).Where("id = ? AND revision = ?", id, revision).Updates(map[string]any{"revision": row.Revision, "identity_revision": row.IdentityRevision, "ciphertext": row.Ciphertext, "installation_hash": row.InstallationHash, "device_hash": row.DeviceHash})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAccountIdentityConflict
	}
	return nil
}

func UpdateUpstreamAccountProfile(ctx context.Context, id string, revision int64, version, userAgent, originator string) error {
	var row UpstreamAccount
	if err := stateDB(ctx).First(&row, "id = ?", id).Error; err != nil {
		return err
	}
	if row.Revision != revision {
		return ErrAccountIdentityConflict
	}
	secret, err := row.Secret()
	if err != nil {
		return err
	}
	secret.ClientVersion, secret.UserAgent, secret.Originator = version, userAgent, originator
	row.Revision++
	row.ProfileRevision++
	if err := sealAccount(&row, secret); err != nil {
		return err
	}
	result := stateDB(ctx).Model(&UpstreamAccount{}).Where("id = ? AND revision = ?", id, revision).
		Updates(map[string]any{"revision": row.Revision, "profile_revision": row.ProfileRevision, "ciphertext": row.Ciphertext})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAccountIdentityConflict
	}
	return nil
}
