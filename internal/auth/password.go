package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

var ErrInvalidPasswordHash = errors.New("invalid password hash")

const (
	argonMemory  uint32 = 64 * 1024
	argonTime    uint32 = 3
	argonThreads uint8  = 1
	argonSaltLen        = 16
	argonKeyLen  uint32 = 32
)

// Default cap on concurrent Argon2id computations; each one holds argonMemory
// (~64MB), so the cap bounds the worst-case memory burst of parallel logins
const DefaultPasswordHashMaxConcurrency = 4

func newPasswordSem(maxConcurrency int) chan struct{} {
	if maxConcurrency == 0 {
		maxConcurrency = DefaultPasswordHashMaxConcurrency
	}
	if maxConcurrency < 0 {
		panic("password hash max concurrency must be positive")
	}
	return make(chan struct{}, maxConcurrency)
}

// Runs HashPassword while holding one of the service's password hashing slots
func (s *Service) hashPassword(ctx context.Context, password string) (string, error) {
	select {
	case s.passwordSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.passwordSem }()
	return HashPassword(password)
}

// Runs VerifyPassword while holding one of the service's password hashing slots
func (s *Service) verifyPassword(ctx context.Context, hash, password string) (bool, error) {
	select {
	case s.passwordSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-s.passwordSem }()
	return VerifyPassword(hash, password)
}

// Hashes the given password with argon2id in PHC string format
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonTime,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verifies the given password against an argon2id PHC hash, reading the
// parameters from the hash itself so they can be tuned without breaking
// previously stored passwords
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrInvalidPasswordHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidPasswordHash
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, ErrInvalidPasswordHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidPasswordHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidPasswordHash
	}

	computed := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(key)))

	return subtle.ConstantTimeCompare(key, computed) == 1, nil
}
