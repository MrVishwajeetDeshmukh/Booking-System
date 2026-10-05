package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	issuer         = "seat-booking"
	audience       = "seat-api"
	TokenLifetime  = 24 * time.Hour
	LocalUserIDKey = "authenticated_user_id"
)

var ErrInvalidToken = errors.New("invalid bearer token")

type tokenHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

type tokenClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Subject  string `json:"sub"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

func IssueToken(secret []byte, subject string, now time.Time) (string, time.Time, error) {
	if len(secret) < 32 || strings.TrimSpace(subject) == "" {
		return "", time.Time{}, ErrInvalidToken
	}
	expiresAt := now.Add(TokenLifetime)
	headerBytes, err := json.Marshal(tokenHeader{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", time.Time{}, err
	}
	claimsBytes, err := json.Marshal(tokenClaims{
		Issuer: issuer, Audience: audience, Subject: subject,
		IssuedAt: now.Unix(), Expires: expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return token, expiresAt, nil
}

func VerifyToken(secret []byte, token string, now time.Time) (string, error) {
	if len(secret) < 32 || len(token) == 0 || len(token) > 4096 {
		return "", ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrInvalidToken
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidToken
	}
	var header tokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return "", ErrInvalidToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", ErrInvalidToken
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", ErrInvalidToken
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	var claims tokenClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return "", ErrInvalidToken
	}
	if claims.Issuer != issuer || claims.Audience != audience || strings.TrimSpace(claims.Subject) == "" ||
		claims.IssuedAt > now.Add(time.Minute).Unix() || claims.Expires <= now.Unix() {
		return "", ErrInvalidToken
	}
	return claims.Subject, nil
}
