package common

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStateCipherOwnerAuthenticationAndKeyRotation(t *testing.T) {
	oldKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	newKey := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	old, err := NewStateCipher("old", map[string]string{"old": oldKey})
	require.NoError(t, err)
	sealed, err := old.Seal("owner-a/checkpoint-1", []byte("private conversation"))
	require.NoError(t, err)
	require.NotContains(t, sealed, "private")
	rotated, err := NewStateCipher("new", map[string]string{"old": oldKey, "new": newKey})
	require.NoError(t, err)
	plain, err := rotated.Open("owner-a/checkpoint-1", sealed)
	require.NoError(t, err)
	require.Equal(t, "private conversation", string(plain))
	_, err = rotated.Open("owner-b/checkpoint-1", sealed)
	require.Error(t, err)
	fresh, err := rotated.Seal("owner-a/checkpoint-1", plain)
	require.NoError(t, err)
	require.Contains(t, fresh, "v1.new.")
	_, err = old.Open("owner-a/checkpoint-1", fresh)
	require.Error(t, err)
}

func TestRelayStateCipherEnvironmentCompatibility(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	legacyKeys := `{"old":"` + key + `"}`
	t.Setenv("RENEWAPI_STATE_KEYS", legacyKeys)
	t.Setenv("RENEWAPI_STATE_ACTIVE_KEY", "old")
	t.Setenv("RILLVEN_STATE_KEYS", "")
	t.Setenv("RILLVEN_STATE_ACTIVE_KEY", "")
	legacy, err := RelayStateCipher()
	require.NoError(t, err)
	sealed, err := legacy.Seal("owner/checkpoint", []byte("existing conversation"))
	require.NoError(t, err)

	// Changing only the variable names must retain access to existing data.
	t.Setenv("RILLVEN_STATE_KEYS", legacyKeys)
	t.Setenv("RILLVEN_STATE_ACTIVE_KEY", "old")
	renamed, err := RelayStateCipher()
	require.NoError(t, err)
	plain, err := renamed.Open("owner/checkpoint", sealed)
	require.NoError(t, err)
	require.Equal(t, "existing conversation", string(plain))

	// A complete new keyring wins over the legacy pair and supports rotation.
	t.Setenv("RILLVEN_STATE_KEYS", `{"old":"`+key+`","new":"`+key+`"}`)
	t.Setenv("RILLVEN_STATE_ACTIVE_KEY", "new")
	rotated, err := RelayStateCipher()
	require.NoError(t, err)
	fresh, err := rotated.Seal("owner/checkpoint", plain)
	require.NoError(t, err)
	require.Contains(t, fresh, "v1.new.")
	_, err = rotated.Open("owner/checkpoint", sealed)
	require.NoError(t, err)

	// Do not borrow a legacy field or return a previously cached valid cipher.
	t.Setenv("RILLVEN_STATE_KEYS", "")
	_, err = RelayStateCipher()
	require.Error(t, err)
	t.Setenv("RILLVEN_STATE_KEYS", legacyKeys)
	t.Setenv("RILLVEN_STATE_ACTIVE_KEY", "")
	_, err = RelayStateCipher()
	require.Error(t, err)
}
