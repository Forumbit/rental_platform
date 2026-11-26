package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	SessionID string `json:"sid"`
	TokenUse  string `json:"token_use"`
	jwt.RegisteredClaims
}

type Identity struct {
	UserID    int64
	SessionID string
}

type TokenManager struct {
	accessKey  []byte
	refreshKey []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	issuer     string
	audience   string
	now        func() time.Time
}

func NewTokenManager(cfg config.AuthConfig) (*TokenManager, error) {
	if len(cfg.AccessSecret) < 32 || len(cfg.RefreshSecret) < 32 {
		return nil, fmt.Errorf("JWT_ACCESS_SECRET and JWT_REFRESH_SECRET must each contain at least 32 bytes")
	}
	if cfg.AccessSecret == cfg.RefreshSecret {
		return nil, fmt.Errorf("access and refresh JWT secrets must be different")
	}
	if cfg.AccessTTL < time.Second || cfg.RefreshTTL <= cfg.AccessTTL ||
		cfg.AccessTTL%time.Second != 0 || cfg.RefreshTTL%time.Second != 0 {
		return nil, fmt.Errorf("JWT TTLs must be whole seconds: access_ttl >= 1s and refresh_ttl > access_ttl")
	}
	if strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.Audience) == "" {
		return nil, fmt.Errorf("JWT issuer and audience must be configured")
	}
	return &TokenManager{
		accessKey: []byte(cfg.AccessSecret), refreshKey: []byte(cfg.RefreshSecret),
		accessTTL: cfg.AccessTTL, refreshTTL: cfg.RefreshTTL,
		issuer: cfg.Issuer, audience: cfg.Audience, now: time.Now,
	}, nil
}

func (m *TokenManager) Issue(userID int64, sessionID string) (*model.TokenPair, error) {
	if userID <= 0 || !validTokenID(sessionID) {
		return nil, fmt.Errorf("invalid user or session for token issuance")
	}
	now := m.now().UTC().Truncate(time.Second)
	pair := &model.TokenPair{
		TokenType: "Bearer", AccessExpiresAt: now.Add(m.accessTTL), RefreshExpiresAt: now.Add(m.refreshTTL),
	}
	var err error
	pair.AccessToken, err = m.sign(userID, sessionID, "access", now, pair.AccessExpiresAt, m.accessKey)
	if err != nil {
		return nil, err
	}
	pair.RefreshToken, err = m.sign(userID, sessionID, "refresh", now, pair.RefreshExpiresAt, m.refreshKey)
	if err != nil {
		return nil, err
	}
	return pair, nil
}

func (m *TokenManager) ParseAccess(token string) (*Identity, error) {
	return m.parse(token, "access", m.accessKey)
}

func (m *TokenManager) ParseRefresh(token string) (*Identity, error) {
	return m.parse(token, "refresh", m.refreshKey)
}

func (m *TokenManager) sign(userID int64, sessionID, tokenUse string, issuedAt, expiresAt time.Time, key []byte) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", err
	}
	claims := Claims{
		SessionID: sessionID, TokenUse: tokenUse,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: m.issuer, Audience: jwt.ClaimStrings{m.audience}, Subject: strconv.FormatInt(userID, 10),
			ID: id, IssuedAt: jwt.NewNumericDate(issuedAt), ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign %s token: %w", tokenUse, err)
	}
	return token, nil
}

func (m *TokenManager) parse(raw, tokenUse string, key []byte) (*Identity, error) {
	if raw == "" || len(raw) > 4096 {
		return nil, model.ErrInvalidToken
	}
	var claims Claims
	token, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithIssuer(m.issuer),
		jwt.WithAudience(m.audience), jwt.WithTimeFunc(m.now),
	)
	if err != nil || !token.Valid || claims.TokenUse != tokenUse ||
		!validTokenID(claims.SessionID) || !validTokenID(claims.ID) ||
		claims.IssuedAt == nil || claims.ExpiresAt == nil || !claims.ExpiresAt.After(claims.IssuedAt.Time) {
		return nil, model.ErrInvalidToken
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || userID <= 0 {
		return nil, model.ErrInvalidToken
	}
	return &Identity{UserID: userID, SessionID: claims.SessionID}, nil
}

func NewSessionID() (string, error) {
	return randomID()
}

func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate token ID: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}

func validTokenID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func HashRefreshToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
