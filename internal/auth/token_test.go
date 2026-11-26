package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/golang-jwt/jwt/v5"
)

func tokenTestConfig() config.AuthConfig {
	return config.AuthConfig{
		AccessSecret: strings.Repeat("a", 32), RefreshSecret: strings.Repeat("r", 32),
		AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour,
		Issuer: "rental-test", Audience: "rental-test-api",
	}
}

func TestTokenPairAndExpiration(t *testing.T) {
	cfg := tokenTestConfig()
	manager, err := NewTokenManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	sessionID, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := manager.Issue(42, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, parse := range []struct {
		fn    func(string) (*Identity, error)
		token string
	}{
		{manager.ParseAccess, pair.AccessToken},
		{manager.ParseRefresh, pair.RefreshToken},
	} {
		identity, err := parse.fn(parse.token)
		if err != nil || identity.UserID != 42 || identity.SessionID != sessionID {
			t.Fatalf("unexpected token identity: %+v, %v", identity, err)
		}
	}
	if pair.TokenType != "Bearer" || !pair.AccessExpiresAt.Equal(now.Add(cfg.AccessTTL)) ||
		!pair.RefreshExpiresAt.Equal(now.Add(cfg.RefreshTTL)) {
		t.Fatalf("incorrect token metadata: %+v", pair)
	}
	second, err := manager.Issue(42, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken == second.AccessToken || pair.RefreshToken == second.RefreshToken {
		t.Fatal("tokens issued in the same second must have unique IDs")
	}
	if _, err := manager.ParseAccess(pair.RefreshToken); !errors.Is(err, model.ErrInvalidToken) {
		t.Fatal("refresh token accepted as an access token")
	}
	if _, err := manager.ParseRefresh(pair.AccessToken); !errors.Is(err, model.ErrInvalidToken) {
		t.Fatal("access token accepted as a refresh token")
	}
	now = pair.AccessExpiresAt
	if _, err := manager.ParseAccess(pair.AccessToken); !errors.Is(err, model.ErrInvalidToken) {
		t.Fatal("expired access token accepted")
	}
	if _, err := manager.ParseRefresh(pair.RefreshToken); err != nil {
		t.Fatalf("refresh must work after access expiration: %v", err)
	}
	now = pair.RefreshExpiresAt
	if _, err := manager.ParseRefresh(pair.RefreshToken); !errors.Is(err, model.ErrInvalidToken) {
		t.Fatal("expired refresh token accepted")
	}
}

func TestRejectInvalidJWT(t *testing.T) {
	cfg := tokenTestConfig()
	manager, err := NewTokenManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	base := Claims{
		SessionID: strings.Repeat("a", 32), TokenUse: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "42", ID: strings.Repeat("b", 32), Issuer: cfg.Issuer,
			Audience: jwt.ClaimStrings{cfg.Audience}, IssuedAt: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
	}
	for _, tt := range []struct {
		name   string
		change func(*Claims)
		method jwt.SigningMethod
		key    any
	}{
		{name: "wrong signature", key: []byte(strings.Repeat("x", 32))},
		{name: "wrong algorithm", method: jwt.SigningMethodHS384},
		{name: "unsigned token", method: jwt.SigningMethodNone, key: jwt.UnsafeAllowNoneSignatureType},
		{name: "wrong token use", change: func(c *Claims) { c.TokenUse = "refresh" }},
		{name: "missing expiration", change: func(c *Claims) { c.ExpiresAt = nil }},
		{name: "expired", change: func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Second)) }},
		{name: "missing issue time", change: func(c *Claims) { c.IssuedAt = nil }},
		{name: "future issue time", change: func(c *Claims) { c.IssuedAt = jwt.NewNumericDate(now.Add(time.Hour)) }},
		{name: "future not before", change: func(c *Claims) { c.NotBefore = jwt.NewNumericDate(now.Add(time.Hour)) }},
		{name: "wrong issuer", change: func(c *Claims) { c.Issuer = "another-app" }},
		{name: "wrong audience", change: func(c *Claims) { c.Audience = jwt.ClaimStrings{"another-api"} }},
		{name: "missing audience", change: func(c *Claims) { c.Audience = nil }},
		{name: "missing session", change: func(c *Claims) { c.SessionID = "" }},
		{name: "missing token id", change: func(c *Claims) { c.ID = "" }},
		{name: "missing subject", change: func(c *Claims) { c.Subject = "" }},
		{name: "zero subject", change: func(c *Claims) { c.Subject = "0" }},
		{name: "negative subject", change: func(c *Claims) { c.Subject = "-1" }},
		{name: "overflowing subject", change: func(c *Claims) { c.Subject = "9223372036854775808" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claims := base
			if tt.change != nil {
				tt.change(&claims)
			}
			method, key := tt.method, tt.key
			if method == nil {
				method = jwt.SigningMethodHS256
			}
			if key == nil {
				key = []byte(cfg.AccessSecret)
			}
			raw, err := jwt.NewWithClaims(method, claims).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ParseAccess(raw); !errors.Is(err, model.ErrInvalidToken) {
				t.Fatalf("token accepted: %v", err)
			}
		})
	}
	for _, raw := range []string{"", "not-a-jwt", strings.Repeat("x", 4097)} {
		if _, err := manager.ParseAccess(raw); !errors.Is(err, model.ErrInvalidToken) {
			t.Fatalf("malformed token accepted: %v", err)
		}
	}
}

func TestRejectInvalidTokenConfiguration(t *testing.T) {
	for _, change := range []func(*config.AuthConfig){
		func(c *config.AuthConfig) { c.AccessSecret = "short" },
		func(c *config.AuthConfig) { c.RefreshSecret = c.AccessSecret },
		func(c *config.AuthConfig) { c.AccessTTL = 0 },
		func(c *config.AuthConfig) { c.RefreshTTL = c.AccessTTL },
		func(c *config.AuthConfig) { c.AccessTTL = time.Millisecond },
		func(c *config.AuthConfig) { c.Issuer = "" },
		func(c *config.AuthConfig) { c.Audience = " " },
	} {
		cfg := tokenTestConfig()
		change(&cfg)
		if _, err := NewTokenManager(cfg); err == nil {
			t.Fatal("invalid token configuration accepted")
		}
	}
}
