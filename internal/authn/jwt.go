package authn

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/config"
)

const (
	AccessTokenTTL = 15 * time.Minute
	jwtIssuer      = "opshub"
	jwtAudience    = "opshub-api"
)

// ErrInvalidAccessToken covers every reason an access token is rejected.
var ErrInvalidAccessToken = errors.New("authn: invalid access token")

type accessClaims struct {
	jwt.RegisteredClaims
	SessionID string `json:"sid"`
}

// JWTSigner issues and verifies EdDSA (Ed25519) access tokens. The first key signs; all
// keys verify, which allows rotating keys without logging everyone out.
type JWTSigner struct {
	activeID string
	private  ed25519.PrivateKey
	public   map[string]ed25519.PublicKey
	now      func() time.Time
}

func NewJWTSigner(keys []config.NamedKey, now func() time.Time) (*JWTSigner, error) {
	if len(keys) == 0 {
		return nil, errors.New("authn: no JWT keys")
	}
	if now == nil {
		now = time.Now
	}
	s := &JWTSigner{activeID: keys[0].ID, public: map[string]ed25519.PublicKey{}, now: now}
	for i, k := range keys {
		if len(k.Key) != ed25519.SeedSize {
			return nil, fmt.Errorf("authn: JWT key %q must be a 32-byte Ed25519 seed", k.ID)
		}
		priv := ed25519.NewKeyFromSeed(k.Key)
		if i == 0 {
			s.private = priv
		}
		s.public[k.ID] = priv.Public().(ed25519.PublicKey)
	}
	return s, nil
}

// Issue returns a signed access token for the user's session and its expiry.
func (s *JWTSigner) Issue(userID, sessionID uuid.UUID) (string, time.Time, error) {
	now := s.now()
	exp := now.Add(AccessTokenTTL)
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Audience:  jwt.ClaimStrings{jwtAudience},
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        uuid.NewString(),
		},
		SessionID: sessionID.String(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = s.activeID
	signed, err := tok.SignedString(s.private)
	return signed, exp, err
}

// Parse verifies signature, algorithm, issuer, audience and expiry.
func (s *JWTSigner) Parse(token string) (userID, sessionID uuid.UUID, err error) {
	var claims accessClaims
	_, err = jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, ok := s.public[kid]
		if !ok {
			return nil, ErrInvalidAccessToken
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(jwtAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, ErrInvalidAccessToken
	}
	if userID, err = uuid.Parse(claims.Subject); err != nil {
		return uuid.Nil, uuid.Nil, ErrInvalidAccessToken
	}
	if sessionID, err = uuid.Parse(claims.SessionID); err != nil {
		return uuid.Nil, uuid.Nil, ErrInvalidAccessToken
	}
	return userID, sessionID, nil
}
