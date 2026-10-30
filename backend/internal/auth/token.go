package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("geçersiz token")

type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// Issuer kısa ömürlü access JWT'leri (HS256) üretir ve doğrular.
type Issuer struct {
	secret    []byte
	accessTTL time.Duration
	now       func() time.Time
}

func NewIssuer(secret string, accessTTL time.Duration) *Issuer {
	return &Issuer{secret: []byte(secret), accessTTL: accessTTL, now: time.Now}
}

func (i *Issuer) AccessTTL() time.Duration { return i.accessTTL }

func (i *Issuer) Access(userID int64, role string) (string, error) {
	now := i.now()
	claims := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.accessTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
}

// Parse imzayı, algoritmayı (yalnızca HS256) ve süreyi doğrular.
func (i *Issuer) Parse(raw string) (Principal, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c,
		func(*jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(i.now),
	)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || c.Role == "" {
		return Principal{}, ErrInvalidToken
	}
	return Principal{UserID: id, Role: c.Role}, nil
}

// NewRefreshToken 256 bit rastgele bir token ve veritabanında saklanacak SHA-256 özetini döner.
func NewRefreshToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashRefreshToken(token), nil
}

func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
