package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Envelope encryption for stored memory content.
//
// Each item is encrypted with its own random 256-bit data key (AES-256-GCM).
// The data key is wrapped by a key-encryption key (KEK) held by a keyProvider
// and stored next to the ciphertext with the KEK's id. Rotating the KEK only
// requires re-wrapping data keys, not re-encrypting content.
//
// keyProvider is the seam for a cloud KMS: a KMS-backed provider implements
// wrap/unwrap with the KMS Encrypt/Decrypt calls. The provider shipped here
// ("local") keeps the KEK in SUEZ_DATA_KEY / SUEZ_DATA_KEY_FILE.
type keyProvider interface {
	keyID() string
	wrap(dataKey, aad []byte) ([]byte, error)
	// unwrap must accept keys wrapped under previous key ids it still holds.
	unwrap(keyID string, wrapped, aad []byte) ([]byte, error)
}

type localKeyProvider struct {
	current string
	keys    map[string][]byte
}

func localKeyID(key []byte) string {
	sum := sha256.Sum256(append([]byte("suez-kek-id:"), key...))
	return "local:" + base64.RawURLEncoding.EncodeToString(sum[:6])
}

func (p *localKeyProvider) keyID() string { return p.current }

func (p *localKeyProvider) wrap(dataKey, aad []byte) ([]byte, error) {
	return gcmSeal(p.keys[p.current], dataKey, aad)
}

func (p *localKeyProvider) unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	key, ok := p.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("key %s is not available; add it to SUEZ_DATA_KEY_PREVIOUS", keyID)
	}
	return gcmOpen(key, wrapped, aad)
}

type contentSealer struct {
	provider keyProvider
}

// sealedContent is what is stored in the database (as JSON bytes).
type sealedContent struct {
	Version    int    `json:"v"`
	KeyID      string `json:"kid"`
	WrappedKey []byte `json:"wk"`
	Ciphertext []byte `json:"ct"`
}

// developmentDataKey is used only when SUEZ_ENV=development and no key is
// configured. It is public, so it protects nothing; it exists so a local
// stack starts without setup. Production refuses to start without a real key.
var developmentDataKey = sha256.Sum256([]byte("suez development data key - not secret"))

func newContentSealer(production bool) (*contentSealer, error) {
	switch provider := strings.ToLower(strings.TrimSpace(env("SUEZ_KEY_PROVIDER", "local"))); provider {
	case "local":
	default:
		return nil, fmt.Errorf("SUEZ_KEY_PROVIDER %q is not supported by this build (supported: local)", provider)
	}
	local := &localKeyProvider{keys: map[string][]byte{}}
	raw := env("SUEZ_DATA_KEY", "")
	if raw == "" {
		if production {
			return nil, errors.New("SUEZ_DATA_KEY (or SUEZ_DATA_KEY_FILE) is required in production")
		}
		slog.Warn("SUEZ_DATA_KEY is not set: using the public development key; stored memory content is NOT protected")
		key := developmentDataKey[:]
		local.current = "local:development"
		local.keys[local.current] = key
		return &contentSealer{provider: local}, nil
	}
	key, err := decodeDataKey(raw)
	if err != nil {
		return nil, err
	}
	local.current = localKeyID(key)
	local.keys[local.current] = key
	for _, previous := range strings.Split(env("SUEZ_DATA_KEY_PREVIOUS", ""), ",") {
		if previous = strings.TrimSpace(previous); previous == "" {
			continue
		}
		old, err := decodeDataKey(previous)
		if err != nil {
			return nil, fmt.Errorf("SUEZ_DATA_KEY_PREVIOUS: %w", err)
		}
		local.keys[localKeyID(old)] = old
	}
	return &contentSealer{provider: local}, nil
}

// seal encrypts plaintext. aad binds the ciphertext to its row (tenant and
// item id), so a ciphertext copied to another tenant's row does not decrypt.
func (s *contentSealer) seal(plaintext []byte, aad string) ([]byte, error) {
	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		return nil, err
	}
	ciphertext, err := gcmSeal(dataKey, plaintext, []byte(aad))
	if err != nil {
		return nil, err
	}
	wrapped, err := s.provider.wrap(dataKey, []byte(aad))
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealedContent{Version: 1, KeyID: s.provider.keyID(), WrappedKey: wrapped, Ciphertext: ciphertext})
}

func (s *contentSealer) open(stored []byte, aad string) ([]byte, error) {
	var sealed sealedContent
	if err := json.Unmarshal(stored, &sealed); err != nil || sealed.Version != 1 {
		return nil, errors.New("stored content is not in a known sealed format")
	}
	dataKey, err := s.provider.unwrap(sealed.KeyID, sealed.WrappedKey, []byte(aad))
	if err != nil {
		return nil, err
	}
	return gcmOpen(dataKey, sealed.Ciphertext, []byte(aad))
}

func gcmSeal(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

func gcmOpen(key, sealed, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("sealed content is truncated")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], aad)
}
