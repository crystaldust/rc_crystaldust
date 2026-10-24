package apikey

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// fixedRandom is an obviously fake, deterministic random source.
var fixedRandom = bytes.Repeat([]byte{0x42}, randomBytes)

func TestGenerate(t *testing.T) {
	// Act
	key, hashHex, err := Generate(bytes.NewReader(fixedRandom))

	// Assert
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if !strings.HasPrefix(key, Prefix) || len(key) != KeyLength {
		t.Errorf("key = %q (len %d), want prefix %q and length %d", key, len(key), Prefix, KeyLength)
	}
	sum := sha256.Sum256([]byte(key))
	if hashHex != hex.EncodeToString(sum[:]) {
		t.Errorf("hash = %s, want sha256 of the key", hashHex)
	}
}

func TestGenerateDiffersPerRandomSource(t *testing.T) {
	a, _, errA := Generate(bytes.NewReader(fixedRandom))
	b, _, errB := Generate(bytes.NewReader(bytes.Repeat([]byte{0x43}, randomBytes)))
	if errA != nil || errB != nil {
		t.Fatalf("Generate errors: %v, %v", errA, errB)
	}
	if a == b {
		t.Error("different random sources produced the same key")
	}
}

func TestGenerateFailsOnShortRandom(t *testing.T) {
	_, _, err := Generate(bytes.NewReader([]byte{1, 2, 3}))
	if err == nil {
		t.Fatal("Generate succeeded with a short random source")
	}
}

func TestHashMatchesSHA256(t *testing.T) {
	const key = "mhk_fake-key-for-tests"
	if Hash(key) != sha256.Sum256([]byte(key)) {
		t.Error("Hash differs from sha256.Sum256")
	}
}

func hexOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func TestParseHashes(t *testing.T) {
	one := hexOf("mhk_old")
	two := hexOf("mhk_new")
	tests := []struct {
		name    string
		value   string
		wantLen int
		wantErr bool
	}{
		{"one hash", one, 1, false},
		{"two hashes", one + "," + two, 2, false},
		{"spaces around comma", one + " , " + two, 2, false},
		{"three hashes", strings.Join([]string{one, two, one}, ","), 0, true},
		{"upper case hex", strings.ToUpper(one), 0, true},
		{"empty item", one + ",", 0, true},
		{"empty value", "", 0, true},
		{"short hash", one[:63], 0, true},
		{"not hex", strings.Repeat("g", 64), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHashes(tt.value)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidHash) {
					t.Fatalf("ParseHashes error = %v, want ErrInvalidHash", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHashes error: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("got %d hashes, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestParseHashesErrorDoesNotEchoValue(t *testing.T) {
	value := strings.Repeat("Z", 64)
	_, err := ParseHashes(value)
	if err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("error = %v, want an error without the value", err)
	}
}

func TestMatches(t *testing.T) {
	// Arrange: rotation keeps the old and the new hash side by side.
	hashes, err := ParseHashes(hexOf("mhk_old") + "," + hexOf("mhk_new"))
	if err != nil {
		t.Fatalf("ParseHashes error: %v", err)
	}
	tests := []struct {
		key  string
		want bool
	}{
		{"mhk_old", true},
		{"mhk_new", true},
		{"mhk_other", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := Matches(tt.key, hashes); got != tt.want {
				t.Errorf("Matches(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
	if Matches("mhk_old", nil) {
		t.Error("Matches with no hashes = true")
	}
}
