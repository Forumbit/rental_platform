package auth

import "golang.org/x/crypto/bcrypt"

// A valid cost-10 hash used only to do comparable work for unknown accounts.
const dummyPasswordHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	if len(password) > 72 {
		return false
	}
	if hash == "" {
		bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
