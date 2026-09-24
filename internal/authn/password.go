// Package authn contains authentication primitives: password hashing and policy, access
// JWTs, TOTP, request principals, and the HTTP authentication / CSRF middleware.
package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the argon2id cost parameters.
type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultArgon2Params follow OWASP guidance (m=64 MiB, t=3, p=2).
var DefaultArgon2Params = Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLen: 16, KeyLen: 32}

var errMalformedHash = errors.New("authn: malformed password hash")

// Hasher hashes and verifies passwords with argon2id (PHC string format).
type Hasher struct {
	Params Argon2Params

	dummyOnce sync.Once
	dummy     string
}

func NewHasher(p Argon2Params) *Hasher { return &Hasher{Params: p} }

// Hash returns "$argon2id$v=19$m=…,t=…,p=…$<salt>$<hash>".
func (h *Hasher) Hash(password string) (string, error) {
	salt := make([]byte, h.Params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := h.Params
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks password against encoded. needsRehash is true when the hash was created
// with weaker parameters than the current ones (upgrade it after a successful login).
func (h *Hasher) Verify(password, encoded string) (ok, needsRehash bool, err error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want))) // #nosec G115 -- len(want) is 16..64, checked in decodeHash
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	cur := h.Params
	needsRehash = p.MemoryKiB < cur.MemoryKiB || p.Iterations < cur.Iterations || p.Parallelism < cur.Parallelism
	return true, needsRehash, nil
}

// VerifyDummy spends the same time as a real verification. Call it when the account does
// not exist so response timing doesn't reveal which emails are registered.
func (h *Hasher) VerifyDummy(password string) {
	h.dummyOnce.Do(func() {
		h.dummy, _ = h.Hash("opshub-dummy-password-for-timing")
	})
	_, _, _ = h.Verify(password, h.dummy)
}

func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Argon2Params{}, nil, nil, errMalformedHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return Argon2Params{}, nil, nil, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, errMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return Argon2Params{}, nil, nil, errMalformedHash
	}
	return p, salt, key, nil
}
