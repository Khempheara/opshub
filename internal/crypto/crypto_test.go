package crypto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/config"
)

func key(id string, b byte) config.NamedKey {
	return config.NamedKey{ID: id, Key: bytes.Repeat([]byte{b}, 32)}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	kr, err := NewKeyRing([]config.NamedKey{key("m1", 1)})
	require.NoError(t, err)

	ct, err := kr.Encrypt([]byte("JBSWY3DPEHPK3PXP"), []byte("totp:u1"))
	require.NoError(t, err)
	assert.NotContains(t, string(ct), "JBSWY3DPEHPK3PXP")

	pt, err := kr.Decrypt(ct, []byte("totp:u1"))
	require.NoError(t, err)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", string(pt))

	ct2, _ := kr.Encrypt([]byte("JBSWY3DPEHPK3PXP"), []byte("totp:u1"))
	assert.NotEqual(t, ct, ct2, "nonces must be random")
}

func TestDecryptRejectsTamperingAndWrongContext(t *testing.T) {
	kr, _ := NewKeyRing([]config.NamedKey{key("m1", 1)})
	ct, _ := kr.Encrypt([]byte("secret"), []byte("totp:u1"))

	_, err := kr.Decrypt(ct, []byte("totp:u2"))
	assert.ErrorIs(t, err, ErrDecrypt, "aad binds ciphertext to its row")

	tampered := bytes.Clone(ct)
	tampered[len(tampered)-1] ^= 1
	_, err = kr.Decrypt(tampered, []byte("totp:u1"))
	assert.ErrorIs(t, err, ErrDecrypt)

	for _, bad := range [][]byte{nil, {1}, {2, 0}, {1, 5, 'x'}, ct[:10]} {
		_, err = kr.Decrypt(bad, []byte("totp:u1"))
		assert.ErrorIs(t, err, ErrDecrypt)
	}
}

func TestKeyRotation(t *testing.T) {
	old, _ := NewKeyRing([]config.NamedKey{key("m1", 1)})
	ct, _ := old.Encrypt([]byte("secret"), nil)

	rotated, _ := NewKeyRing([]config.NamedKey{key("m2", 2), key("m1", 1)})
	assert.Equal(t, "m2", rotated.ActiveKeyID())
	pt, err := rotated.Decrypt(ct, nil)
	require.NoError(t, err, "old ciphertexts stay readable after rotation")
	assert.Equal(t, "secret", string(pt))

	removed, _ := NewKeyRing([]config.NamedKey{key("m2", 2)})
	_, err = removed.Decrypt(ct, nil)
	assert.ErrorIs(t, err, ErrDecrypt)
}

func TestRandomTokenAndHash(t *testing.T) {
	a, b := RandomToken(32), RandomToken(32)
	assert.Len(t, a, 43)
	assert.NotEqual(t, a, b)
	assert.Len(t, HashToken(a), 32)
	assert.Equal(t, HashToken(a), HashToken(a))
}
