package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
)

// Envelope is a value sealed with its own random data key (DEK), and the DEK sealed by the
// key ring. Rotating the master key only re-wraps the DEK; the value's ciphertext stays.
type Envelope struct {
	Ciphertext []byte // AES-256-GCM of the value under the DEK (with tag)
	Nonce      []byte
	DEKEnc     []byte // the DEK, sealed by the key ring
	KEKID      string // the key ring key that sealed the DEK
}

// SealEnvelope encrypts plaintext under a fresh DEK. aad binds both layers to their row.
func (kr *KeyRing) SealEnvelope(plaintext, aad []byte) (Envelope, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return Envelope{}, err
	}
	defer clear(dek)
	aead, err := dekAEAD(dek)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, err
	}
	wrapped, err := kr.Encrypt(dek, aad)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Ciphertext: aead.Seal(nil, nonce, plaintext, aad), Nonce: nonce, DEKEnc: wrapped, KEKID: kr.active}, nil
}

// OpenEnvelope unwraps the DEK and decrypts the value.
func (kr *KeyRing) OpenEnvelope(e Envelope, aad []byte) ([]byte, error) {
	dek, err := kr.Decrypt(e.DEKEnc, aad)
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	aead, err := dekAEAD(dek)
	if err != nil || len(e.Nonce) != aead.NonceSize() {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, e.Nonce, e.Ciphertext, aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Rewrap re-seals a value written by Encrypt (or an envelope's DEK) with the active key. It
// returns changed = false when the value already uses the active key.
func (kr *KeyRing) Rewrap(ciphertext, aad []byte) (out []byte, changed bool, err error) {
	if KeyIDOf(ciphertext) == kr.active {
		return ciphertext, false, nil
	}
	pt, err := kr.Decrypt(ciphertext, aad)
	if err != nil {
		return nil, false, err
	}
	defer clear(pt)
	out, err = kr.Encrypt(pt, aad)
	return out, err == nil, err
}

// KeyIDOf returns the key ring key id recorded in a value written by Encrypt ("" if malformed).
func KeyIDOf(ciphertext []byte) string {
	if len(ciphertext) < 2 || ciphertext[0] != formatV1 || len(ciphertext) < 2+int(ciphertext[1]) {
		return ""
	}
	return string(ciphertext[2 : 2+int(ciphertext[1])])
}

func dekAEAD(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
