package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
)

// StateCipher encrypts durable relay state with a separately managed key ring.
// Keys are never stored with database records. Retain old keys until all records
// using them have expired or been rewritten. AAD prevents swapping tenant rows.
type StateCipher struct {
	active string
	keys   map[string]cipher.AEAD
}

func NewStateCipher(active string, encoded map[string]string) (*StateCipher, error) {
	if len(encoded) == 0 || len(encoded) > 16 {
		return nil, errors.New("state encryption requires 1-16 keys")
	}
	c := &StateCipher{active: active, keys: make(map[string]cipher.AEAD, len(encoded))}
	for id, value := range encoded {
		if len(id) == 0 || len(id) > 32 || strings.ContainsAny(id, ".:/\\ \t\r\n") {
			return nil, errors.New("invalid state encryption key identifier")
		}
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			return nil, errors.New("state encryption keys must be base64 encoded 32-byte keys")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		c.keys[id], err = cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
	}
	if c.keys[active] == nil {
		return nil, errors.New("active state encryption key is unavailable")
	}
	return c, nil
}

func (c *StateCipher) Seal(aad string, plaintext []byte) (string, error) {
	gcm := c.keys[c.active]
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	prefix := "v1." + c.active + "."
	sealed := gcm.Seal(nonce, nonce, plaintext, []byte(prefix+aad))
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (c *StateCipher) Open(aad, encrypted string) ([]byte, error) {
	parts := strings.SplitN(encrypted, ".", 3)
	if len(parts) != 3 || parts[0] != "v1" || c.keys[parts[1]] == nil {
		return nil, errors.New("state ciphertext version or key is unavailable")
	}
	gcm := c.keys[parts[1]]
	sealed, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("invalid state ciphertext")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(parts[0]+"."+parts[1]+"."+aad))
	if err != nil {
		return nil, errors.New("state ciphertext authentication failed")
	}
	return plain, nil
}

var stateCipherCache struct {
	sync.RWMutex
	raw, active string
	cipher      *StateCipher
}

func RelayStateCipher() (*StateCipher, error) {
	raw, active := os.Getenv("RENEWAPI_STATE_KEYS"), os.Getenv("RENEWAPI_STATE_ACTIVE_KEY")
	stateCipherCache.RLock()
	if raw == stateCipherCache.raw && active == stateCipherCache.active && stateCipherCache.cipher != nil {
		c := stateCipherCache.cipher
		stateCipherCache.RUnlock()
		return c, nil
	}
	stateCipherCache.RUnlock()
	var keys map[string]string
	if raw == "" || Unmarshal([]byte(raw), &keys) != nil {
		return nil, errors.New("configure RENEWAPI_STATE_KEYS and RENEWAPI_STATE_ACTIVE_KEY before enabling durable relay state")
	}
	c, err := NewStateCipher(active, keys)
	if err != nil {
		return nil, err
	}
	stateCipherCache.Lock()
	stateCipherCache.raw, stateCipherCache.active, stateCipherCache.cipher = raw, active, c
	stateCipherCache.Unlock()
	return c, nil
}
