// Package apikey generates inbound API keys and checks them against the
// SHA-256 hashes held in the environment (ADR-0008 D4). It depends only on
// the standard library.
package apikey

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	// Prefix marks a Message Hub key, so leaked keys are easy to scan for.
	Prefix = "mhk_"
	// randomBytes is the key entropy: 256 bits, which is why an unsalted
	// SHA-256 is enough to store it.
	randomBytes = 32
	// KeyLength is len(Prefix) plus the unpadded base64url of randomBytes.
	KeyLength = len(Prefix) + 43
	// MaxHashes allows an old and a new key side by side during rotation.
	MaxHashes = 2
)

// ErrInvalidHash is returned when a hash list is not 1–2 comma separated
// 64-character lower-case hex values.
var ErrInvalidHash = errors.New("invalid api key hash list")

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Generate returns a new key and the lower-case hex SHA-256 of it. rand is
// crypto/rand.Reader in production.
func Generate(rand io.Reader) (key, hashHex string, err error) {
	buf := make([]byte, randomBytes)
	if _, err := io.ReadFull(rand, buf); err != nil {
		return "", "", fmt.Errorf("read random bytes: %w", err)
	}
	key = Prefix + base64.RawURLEncoding.EncodeToString(buf)
	sum := Hash(key)
	return key, hex.EncodeToString(sum[:]), nil
}

// Hash returns the SHA-256 of key.
func Hash(key string) [32]byte {
	return sha256.Sum256([]byte(key))
}

// ParseHashes parses the value of a principal's hash variable. Errors give
// the position of the bad item but never the value itself.
func ParseHashes(value string) ([][32]byte, error) {
	items := strings.Split(value, ",")
	if len(items) > MaxHashes {
		return nil, fmt.Errorf("%w: %d hashes, want at most %d", ErrInvalidHash, len(items), MaxHashes)
	}
	out := make([][32]byte, 0, len(items))
	for i, item := range items {
		item = strings.TrimSpace(item)
		if !hashPattern.MatchString(item) {
			return nil, fmt.Errorf("%w: item %d is not 64 lower-case hex characters", ErrInvalidHash, i+1)
		}
		var h [32]byte
		// The pattern above guarantees valid hex of exactly 32 bytes, so
		// Decode cannot fail; its count and error are not needed.
		_, _ = hex.Decode(h[:], []byte(item))
		out = append(out, h)
	}
	return out, nil
}

// Matches reports whether key hashes to any of hashes. Every hash is
// compared in constant time, so timing does not reveal which one matched.
func Matches(key string, hashes [][32]byte) bool {
	sum := Hash(key)
	matched := 0
	for _, h := range hashes {
		matched |= subtle.ConstantTimeCompare(sum[:], h[:])
	}
	return matched == 1
}
