// Package crypto provides authenticated encryption for data at rest (TOTP seeds, provider
// tokens, deploy credentials, secrets) and random token helpers.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/opshub/opshub/internal/config"
)

// ErrDecrypt is returned for tampered, truncated or unknown-key ciphertexts.
var ErrDecrypt = errors.New("crypto: cannot decrypt value")

const formatV1 byte = 1

// KeyRing encrypts with the active key and decrypts with any key it knows, so master keys
// can be rotated without downtime.
type KeyRing struct {
	active string
	aeads  map[string]cipher.AEAD
}

func NewKeyRing(keys []config.NamedKey) (*KeyRing, error) {
	if len(keys) == 0 {
		return nil, errors.New("crypto: empty key ring")
	}
	kr := &KeyRing{active: keys[0].ID, aeads: make(map[string]cipher.AEAD, len(keys))}
	for _, k := range keys {
		if k.ID == "" || len(k.ID) > 255 {
			return nil, fmt.Errorf("crypto: key id %q must be 1-255 bytes", k.ID)
		}
		block, err := aes.NewCipher(k.Key)
		if err != nil {
			return nil, fmt.Errorf("crypto: key %q: %w", k.ID, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		kr.aeads[k.ID] = aead
	}
	return kr, nil
}

// ActiveKeyID is the key new ciphertexts are written with.
func (kr *KeyRing) ActiveKeyID() string { return kr.active }

// Encrypt seals plaintext with AES-256-GCM. aad binds the ciphertext to its context (e.g.
// "totp:<user id>") so it cannot be moved to another row.
// Layout: version(1) | len(keyID)(1) | keyID | nonce(12) | ciphertext+tag.
func (kr *KeyRing) Encrypt(plaintext, aad []byte) ([]byte, error) {
	aead := kr.aeads[kr.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 2+len(kr.active)+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, formatV1, byte(len(kr.active))) // #nosec G115 -- key id length ≤ 255, checked in NewKeyRing
	out = append(out, kr.active...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

// Decrypt opens a value produced by Encrypt with the same aad.
func (kr *KeyRing) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < 2 || ciphertext[0] != formatV1 {
		return nil, ErrDecrypt
	}
	idLen := int(ciphertext[1])
	if len(ciphertext) < 2+idLen {
		return nil, ErrDecrypt
	}
	aead, ok := kr.aeads[string(ciphertext[2:2+idLen])]
	if !ok {
		return nil, ErrDecrypt
	}
	rest := ciphertext[2+idLen:]
	if len(rest) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// RandomToken returns n random bytes encoded as unpadded base64url.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err)) // unrecoverable: the OS CSPRNG is broken
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomBase32 returns n random characters from the RFC 4648 base32 alphabet (A–Z, 2–7),
// which avoids look-alike characters in codes people type.
func RandomBase32(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	for i := range b {
		b[i] = alphabet[b[i]&31] // 256 is a multiple of 32: no modulo bias
	}
	return string(b)
}

// HashToken is the lookup hash stored for bearer-style tokens (session, API, email, runner).
// Tokens have ≥256 bits of entropy, so a fast hash is appropriate (no password stretching).
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
