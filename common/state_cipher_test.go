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
