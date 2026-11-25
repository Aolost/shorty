package urlval

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAndNormalize_Valid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "basic https",
			in:   "https://example.com/path?a=1&b=2#frag",
			want: "https://example.com/path?a=1&b=2#frag",
		},
		{
			name: "uppercase scheme and host normalized",
			in:   "HTTPS://Example.COM/Path",
			want: "https://example.com/Path",
		},
		{
			name: "default https port stripped",
			in:   "https://example.com:443/hello",
			want: "https://example.com/hello",
		},
		{
			name: "default http port stripped",
			in:   "http://example.com:80/hello",
			want: "http://example.com/hello",
		},
		{
			name: "non-default port preserved",
			in:   "https://example.com:8443/hello",
			want: "https://example.com:8443/hello",
		},
		{
			name: "ipv4 host",
			in:   "http://127.0.0.1:8080/test",
			want: "http://127.0.0.1:8080/test",
		},
		{
			name: "ipv6 host",
			in:   "http://[::1]:8080/test",
			want: "http://[::1]:8080/test",
		},
		{
			name: "ipv6 default port stripped",
			in:   "https://[2001:DB8::1]:443/",
			want: "https://[2001:db8::1]/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateAndNormalize(tc.in, DefaultMaxURLLength)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateAndNormalize_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"empty", "", ErrEmptyURL},
		{"whitespace", "   ", ErrInvalidCharacters},
		{"leading space", " https://example.com", ErrInvalidCharacters},
		{"trailing newline", "https://example.com\n", ErrInvalidCharacters},
		{"control char cr", "https://example.com/\rfoo", ErrInvalidCharacters},
		{"control char null", "https://example.com/\x00foo", ErrInvalidCharacters},
		{"tab in scheme", "ht\ttp://example.com", ErrInvalidCharacters},
		{"unicode homoglyph", "https://exаmple.com", ErrInvalidCharacters},
		{"backslash in authority", `https://evil.com\@example.com`, ErrInvalidCharacters},
		{"backslash in path", `https://example.com\admin`, ErrInvalidCharacters},
		{"javascript scheme", "javascript:alert(1)", ErrUnsupportedScheme},
		{"data scheme", "data:text/html,<script>alert(1)</script>", ErrUnsupportedScheme},
		{"file scheme", "file:///etc/passwd", ErrUnsupportedScheme},
		{"ftp scheme", "ftp://example.com/file", ErrUnsupportedScheme},
		{"scheme relative", "//example.com/path", ErrUnsupportedScheme},
		{"relative path", "/local/path", ErrUnsupportedScheme},
		{"opaque http", "http:example.com", ErrUnsupportedScheme},
		{"userinfo user:pass", "https://user:pass@example.com/", ErrCredentials},
		{"userinfo user only", "https://user@example.com/", ErrCredentials},
		{"empty userinfo @", "https://@example.com/", ErrCredentials},
		{"empty host", "https:///path", ErrInvalidHost},
		{"empty port", "https://example.com:/path", ErrInvalidPort},
		{"port zero", "https://example.com:0/path", ErrInvalidPort},
		{"port too high", "https://example.com:65536/path", ErrInvalidPort},
		{"port non numeric", "https://example.com:abc/path", ErrInvalidURL},
		{"double colon", "https://example.com:80:80/path", ErrInvalidPort},
		{"trailing dot host", "https://example.com./path", ErrInvalidHost},
		{"double dot host", "https://example..com/path", ErrInvalidHost},
		{"hyphen start label", "https://-example.com/path", ErrInvalidHost},
		{"malformed ipv6", "https://[::1/path", ErrInvalidURL},
		{"numeric tld", "https://example.123/path", ErrInvalidHost},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateAndNormalize(tc.in, DefaultMaxURLLength)
			if err == nil {
				t.Fatalf("expected error %v, got valid URL %q", tc.wantErr, got)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAndNormalize_MaxLength(t *testing.T) {
	base := "https://example.com/"
	tooLong := base + strings.Repeat("a", 2049-len(base))
	if _, err := ValidateAndNormalize(tooLong, 2048); !errors.Is(err, ErrURLTooLong) {
		t.Fatalf("expected ErrURLTooLong, got %v", err)
	}
	exact := base + strings.Repeat("a", 2048-len(base))
	if _, err := ValidateAndNormalize(exact, 2048); err != nil {
		t.Fatalf("expected exact length to succeed, got %v", err)
	}
}

func FuzzValidateURL(f *testing.F) {
	seeds := []string{
		"https://example.com/path?q=1",
		"http://127.0.0.1:8080/",
		"https://[::1]:443/a",
		"javascript:alert(1)",
		"https://user:pass@evil.com",
		"https://evil.com\\@good.com",
		"//evil.com",
		"https://example.com:99999",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		canonical, err := ValidateAndNormalize(raw, DefaultMaxURLLength)
		if err != nil {
			return
		}
		if len(canonical) > DefaultMaxURLLength {
			t.Fatalf("canonical URL exceeds max length: %d", len(canonical))
		}
		if !strings.HasPrefix(canonical, "http://") && !strings.HasPrefix(canonical, "https://") {
			t.Fatalf("canonical URL has invalid scheme: %q", canonical)
		}
		// Must be idempotent.
		again, err := ValidateAndNormalize(canonical, DefaultMaxURLLength)
		if err != nil {
			t.Fatalf("canonical URL failed re-validation: %q -> %v", canonical, err)
		}
		if again != canonical {
			t.Fatalf("normalization not idempotent: %q -> %q", canonical, again)
		}
	})
}
