package shortcode

import (
	"crypto/rand"
	"errors"
	"fmt"
)

const (
	// Alphabet is a 64-character URL-safe alphabet (power of two to avoid modulo bias).
	Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	// DefaultLength is 11 characters (66 bits of cryptographic entropy).
	DefaultLength = 11
	MinLength     = 8
	MaxLength     = 32
)

var ErrInvalidLength = errors.New("code length out of bounds")

// Generate returns a cryptographically random, URL-safe short code of length n.
// It never derives codes from any destination URL or predictable counter.
func Generate(n int) (string, error) {
	if n < MinLength || n > MaxLength {
		return "", fmt.Errorf("%w: must be between %d and %d", ErrInvalidLength, MinLength, MaxLength)
	}

	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	out := make([]byte, n)
	for i, b := range buf {
		out[i] = Alphabet[b&63]
	}
	return string(out), nil
}

// IsValid checks whether code matches the expected URL-safe alphabet and length bounds.
func IsValid(code string) bool {
	if len(code) < MinLength || len(code) > MaxLength {
		return false
	}
	for i := 0; i < len(code); i++ {
		if !isAlphabetByte(code[i]) {
			return false
		}
	}
	return true
}

func isAlphabetByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_'
}
