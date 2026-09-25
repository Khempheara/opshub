package crypto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/config"
)

func TestEnvelopeRoundTripAndTampering(t *testing.T) {
	kr, err := NewKeyRing([]config.NamedKey{key("m1", 1)})
	require.NoError(t, err)
	aad := []byte("secret:s1:1")

	e, err := kr.SealEnvelope([]byte("postgres://u:p@db/app"), aad)
	require.NoError(t, err)
	assert.Equal(t, "m1", e.KEKID)
	assert.Equal(t, "m1", KeyIDOf(e.DEKEnc))
	assert.NotContains(t, string(e.Ciphertext), "postgres")

	pt, err := kr.OpenEnvelope(e, aad)
	require.NoError(t, err)
	assert.Equal(t, "postgres://u:p@db/app", string(pt))

	e2, _ := kr.SealEnvelope([]byte("postgres://u:p@db/app"), aad)
	assert.NotEqual(t, e.DEKEnc, e2.DEKEnc, "a fresh DEK per value")
	assert.NotEqual(t, e.Ciphertext, e2.Ciphertext)

	_, err = kr.OpenEnvelope(e, []byte("secret:s1:2"))
	assert.ErrorIs(t, err, ErrDecrypt, "aad binds the value to its version")

	bad := e
	bad.Ciphertext = bytes.Clone(e.Ciphertext)
	bad.Ciphertext[0] ^= 1
	_, err = kr.OpenEnvelope(bad, aad)
	assert.ErrorIs(t, err, ErrDecrypt)

	bad = e
	bad.Nonce = e.Nonce[:4]
	_, err = kr.OpenEnvelope(bad, aad)
	assert.ErrorIs(t, err, ErrDecrypt)

	// Swapping in another value's DEK fails.
	bad = e
	bad.DEKEnc = e2.DEKEnc
	_, err = kr.OpenEnvelope(bad, aad)
	assert.ErrorIs(t, err, ErrDecrypt)
}

func TestRewrapMovesToTheActiveKey(t *testing.T) {
	old, _ := NewKeyRing([]config.NamedKey{key("m1", 1)})
	ct, _ := old.Encrypt([]byte("token"), []byte("ctx"))
	env, _ := old.SealEnvelope([]byte("value"), []byte("env"))

	both, _ := NewKeyRing([]config.NamedKey{key("m2", 2), key("m1", 1)})
	out, changed, err := both.Rewrap(ct, []byte("ctx"))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "m2", KeyIDOf(out))

	again, changed, err := both.Rewrap(out, []byte("ctx"))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, out, again)

	_, _, err = both.Rewrap(ct, []byte("other"))
	assert.ErrorIs(t, err, ErrDecrypt)

	env.DEKEnc, changed, err = both.Rewrap(env.DEKEnc, []byte("env"))
	require.NoError(t, err)
	assert.True(t, changed)

	// After rotation the old key can be dropped.
	onlyNew, _ := NewKeyRing([]config.NamedKey{key("m2", 2)})
	pt, err := onlyNew.Decrypt(out, []byte("ctx"))
	require.NoError(t, err)
	assert.Equal(t, "token", string(pt))
	v, err := onlyNew.OpenEnvelope(env, []byte("env"))
	require.NoError(t, err)
	assert.Equal(t, "value", string(v))

	assert.Empty(t, KeyIDOf(nil))
	assert.Empty(t, KeyIDOf([]byte{9, 1}))
	assert.Empty(t, KeyIDOf([]byte{formatV1, 200, 'x'}))
}
