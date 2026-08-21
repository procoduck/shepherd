package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// HashPassword hashes pw with argon2id and returns the encoded string.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	hash := argon2.IDKey([]byte(pw), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

// VerifyPassword returns true if pw matches the encoded argon2id hash.
func VerifyPassword(encodedHash, pw string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid argon2id hash format")
	}
	var memory, itime, threads uint32
	for _, kv := range strings.Split(parts[3], ",") {
		kv2 := strings.SplitN(kv, "=", 2)
		if len(kv2) != 2 {
			continue
		}
		v, err := strconv.ParseUint(kv2[1], 10, 32)
		if err != nil {
			return false, fmt.Errorf("parsing argon2id params: %w", err)
		}
		switch kv2[0] {
		case "m":
			memory = uint32(v)
		case "t":
			itime = uint32(v)
		case "p":
			threads = uint32(v)
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decoding salt: %w", err)
	}
	storedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decoding hash: %w", err)
	}
	computed := argon2.IDKey([]byte(pw), salt, itime, memory, uint8(threads), uint32(len(storedHash))) //nolint:gosec // encoded hash controls a bounded output length
	return subtle.ConstantTimeCompare(computed, storedHash) == 1, nil
}
