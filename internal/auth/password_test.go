package auth

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordHashing(t *testing.T) {
	password := "correct-horse-battery-staple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if hash == password || !CheckPassword(hash, password) || CheckPassword(hash, "wrong-password") {
		t.Fatal("password hashing or verification failed")
	}
	if CheckPassword(hash, password+strings.Repeat("x", 72)) || CheckPassword("", password) {
		t.Fatal("invalid credentials were accepted")
	}
	if _, err := HashPassword(strings.Repeat("x", 73)); !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatal("overlong password was silently truncated")
	}
	if cost, err := bcrypt.Cost([]byte(dummyPasswordHash)); err != nil || cost != bcrypt.DefaultCost {
		t.Fatal("dummy hash must match the password hashing cost")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte("invalid-password")); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("dummy hash does not perform a valid bcrypt comparison: %v", err)
	}
}
