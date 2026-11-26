package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/forumbit/rental_platform/internal/model"
)

type VerificationManager struct {
	key []byte
	ttl time.Duration
}

func NewVerificationManager(secret string, ttl time.Duration) (*VerificationManager, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("EMAIL_VERIFICATION_SECRET must contain at least 32 bytes")
	}
	if ttl < time.Minute || ttl > 24*time.Hour || ttl%time.Minute != 0 {
		return nil, fmt.Errorf("email verification TTL must be whole minutes between 1m and 24h")
	}
	return &VerificationManager{key: []byte(secret), ttl: ttl}, nil
}

func (m *VerificationManager) Generate(userID int64) (model.EmailVerification, string, string, error) {
	id, err := randomID()
	if err != nil {
		return model.EmailVerification{}, "", "", err
	}
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return model.EmailVerification{}, "", "", fmt.Errorf("generate verification token: %w", err)
	}
	token := hex.EncodeToString(bytes[:])
	number, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return model.EmailVerification{}, "", "", fmt.Errorf("generate verification code: %w", err)
	}
	code := fmt.Sprintf("%06d", number.Int64())
	return model.EmailVerification{
		UserID: userID, ID: id, TokenHash: HashVerificationToken(token),
		CodeHash: m.HashCode(userID, id, code), ExpiresAt: time.Now().UTC().Add(m.ttl),
	}, code, token, nil
}

// A keyed, challenge-specific digest protects a short code from offline guessing.
func (m *VerificationManager) HashCode(userID int64, id, code string) []byte {
	hash := hmac.New(sha256.New, m.key)
	hash.Write([]byte("email-verification:" + strconv.FormatInt(userID, 10) + ":" + id + ":" + code))
	return hash.Sum(nil)
}

func HashVerificationToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
