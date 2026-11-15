package model

import (
	"context"
	"crypto/rand"
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

type ChannelIdentity struct {
	ChannelID        int     `gorm:"primaryKey;autoIncrement:false" json:"channel_id"`
	SpaceID          string  `gorm:"size:36;not null" json:"space_id"`
	Revision         int64   `gorm:"not null" json:"revision"`
	Ciphertext       string  `gorm:"type:text;not null" json:"-"`
	InstallationHash *string `gorm:"size:64;uniqueIndex" json:"-"`
	DeviceHash       *string `gorm:"size:64;uniqueIndex" json:"-"`
}

type ChannelIdentitySecret struct {
	Seed           string `json:"seed"`
	SlotSeed       string `json:"slot_seed"`
	InstallationID string `json:"installation_id,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
}

type ChannelCredentialSlot struct {
	ID          string `gorm:"size:36;primaryKey" json:"id"`
	ChannelID   int    `gorm:"uniqueIndex:idx_channel_slot_subject;not null" json:"channel_id"`
	SubjectHash string `gorm:"size:64;uniqueIndex:idx_channel_slot_subject;not null" json:"-"`
}

func (r ChannelIdentity) aad() string {
	return fmt.Sprintf("channel-identity/%d/%s/%d", r.ChannelID, r.SpaceID, r.Revision)
}

func (r ChannelIdentity) Secret() (ChannelIdentitySecret, error) {
	var secret ChannelIdentitySecret
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return secret, err
	}
	raw, err := cipher.Open(r.aad(), r.Ciphertext)
	if err != nil {
		return secret, err
	}
	if err = common.Unmarshal(raw, &secret); err != nil {
		return secret, errors.New("invalid channel identity state")
	}
	seed, err := base64.StdEncoding.DecodeString(secret.Seed)
	if err != nil || len(seed) != 32 {
		return secret, errors.New("invalid channel identity seed")
	}
	return secret, nil
}

func encryptChannelIdentity(r *ChannelIdentity, secret ChannelIdentitySecret) error {
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return err
	}
	raw, err := common.Marshal(secret)
	if err != nil {
		return err
	}
	r.Ciphertext, err = cipher.Seal(r.aad(), raw)
	return err
}

func EnsureChannelIdentity(ctx context.Context, channelID int) (*ChannelIdentity, error) {
	if DB == nil || channelID <= 0 {
		return nil, errors.New("channel identity database or channel is unavailable")
	}
	var row ChannelIdentity
	err := stateDB(ctx).First(&row, "channel_id = ?", channelID).Error
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	seed := make([]byte, 64)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	row = ChannelIdentity{ChannelID: channelID, SpaceID: uuid.NewString(), Revision: 1}
	if err := encryptChannelIdentity(&row, ChannelIdentitySecret{Seed: base64.StdEncoding.EncodeToString(seed[:32]), SlotSeed: base64.StdEncoding.EncodeToString(seed[32:])}); err != nil {
		return nil, err
	}
	// Concurrent first requests use the persisted winner, never their private seed.
	if err := stateDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	if err := stateDB(ctx).First(&row, "channel_id = ?", channelID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func EnsureChannelCredentialSlot(ctx context.Context, channelID int, subjectHash string) (*ChannelCredentialSlot, error) {
	var slot ChannelCredentialSlot
	err := stateDB(ctx).Where("channel_id = ? AND subject_hash = ?", channelID, subjectHash).First(&slot).Error
	if err == nil {
		return &slot, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	slot = ChannelCredentialSlot{ID: uuid.NewString(), ChannelID: channelID, SubjectHash: subjectHash}
	if err := stateDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&slot).Error; err != nil {
		return nil, err
	}
	// The attempted UUID is not a filter when another process won insertion.
	slot = ChannelCredentialSlot{}
	if err := stateDB(ctx).Where("channel_id = ? AND subject_hash = ?", channelID, subjectHash).First(&slot).Error; err != nil {
		return nil, err
	}
	return &slot, nil
}

// UpdateChannelIdentity uses an explicit revision so stale admin forms cannot
// undo a newer reset. An identity reset changes all derived upstream sessions.
func UpdateChannelIdentity(ctx context.Context, channelID int, revision int64, installationID, deviceID string, reset bool) error {
	if installationID != "" {
		parsed, err := uuid.Parse(installationID)
		if err != nil {
			return errors.New("installation ID must be a UUID")
		}
		installationID = parsed.String()
	}
	if len(deviceID) > 128 {
		return errors.New("device ID exceeds 128 characters")
	}
	for _, ch := range deviceID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return errors.New("device ID must contain only letters, digits, underscores or hyphens")
		}
	}
	row, err := EnsureChannelIdentity(ctx, channelID)
	if err != nil {
		return err
	}
	if row.Revision != revision {
		return errors.New("channel identity changed; reload before saving")
	}
	secret, err := row.Secret()
	if err != nil {
		return err
	}
	if reset {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return err
		}
		secret.Seed = base64.StdEncoding.EncodeToString(seed)
	}
	secret.InstallationID, secret.DeviceID = installationID, deviceID
	row.Revision++
	if err := encryptChannelIdentity(row, secret); err != nil {
		return err
	}
	result := stateDB(ctx).Model(&ChannelIdentity{}).Where("channel_id = ? AND revision = ?", channelID, revision).
		Updates(map[string]any{"revision": row.Revision, "ciphertext": row.Ciphertext,
			"installation_hash": optionalIdentityHash(installationID), "device_hash": optionalIdentityHash(deviceID)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("channel identity changed; reload before saving")
	}
	return nil
}

func optionalIdentityHash(value string) *string {
	if value == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(sum[:])
	return &hash
}
