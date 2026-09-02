package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
)

const (
	// The OWASP figure for PBKDF2-HMAC-SHA256, and about 60ms of work here.
	// A service tunes it to its own hardware, and prefers argon2id when it can
	// take the dependency: golang.org/x/crypto/argon2 is not in the standard
	// library, and crypto/pbkdf2 is.
	pbkdf2Iterations = 600_000

	saltLen = 16
	keyLen  = 32
)

func newSalt() []byte {
	salt := make([]byte, saltLen)
	//nolint:errcheck // crypto/rand.Read never fails; it crashes the program.
	rand.Read(salt)
	return salt
}

func derive(password string, salt []byte) []byte {
	// keyLen is a constant above zero, so Key cannot fail here.
	sum, _ := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, keyLen)
	return sum
}

// passwordMatches compares in constant time, so the answer takes the same
// time whatever the first wrong byte is.
func passwordMatches(password string, salt, want []byte) bool {
	return subtle.ConstantTimeCompare(derive(password, salt), want) == 1
}
