package shortcode

import (
	"errors"
	"testing"
)

func TestGenerate_UniquenessAndFormat(t *testing.T) {
	const count = 10000
	seen := make(map[string]struct{}, count)

	for i := 0; i < count; i++ {
		code, err := Generate(DefaultLength)
		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}
		if len(code) != DefaultLength {
			t.Fatalf("got length %d, want %d", len(code), DefaultLength)
		}
		if !IsValid(code) {
			t.Fatalf("generated invalid code %q", code)
		}
		if _, exists := seen[code]; exists {
			t.Fatalf("duplicate code generated: %q", code)
		}
		seen[code] = struct{}{}
	}
}

func TestGenerate_Bounds(t *testing.T) {
	if _, err := Generate(MinLength - 1); !errors.Is(err, ErrInvalidLength) {
		t.Fatalf("expected ErrInvalidLength below min, got %v", err)
	}
	if _, err := Generate(MaxLength + 1); !errors.Is(err, ErrInvalidLength) {
		t.Fatalf("expected ErrInvalidLength above max, got %v", err)
	}
	for _, l := range []int{MinLength, DefaultLength, MaxLength} {
		code, err := Generate(l)
		if err != nil {
			t.Fatalf("Generate(%d) unexpected error: %v", l, err)
		}
		if len(code) != l {
			t.Fatalf("Generate(%d) got len %d", l, len(code))
		}
	}
}

func TestIsValid(t *testing.T) {
	valid := []string{"AbCdEf12", "a_b-C123456"}
	for _, c := range valid {
		if !IsValid(c) {
			t.Errorf("expected %q to be valid", c)
		}
	}
	invalid := []string{"", "short", "has space!", "has/slash12", "../etc/passwd", "abc?x=123"}
	for _, c := range invalid {
		if IsValid(c) {
			t.Errorf("expected %q to be invalid", c)
		}
	}
}
