package urlval

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	// DefaultMaxURLLength is the default maximum accepted URL length in bytes.
	DefaultMaxURLLength = 2048
	// MinMaxURLLength is the lower bound for configurable max URL length.
	MinMaxURLLength = 64
	// MaxMaxURLLength is the upper bound for configurable max URL length.
	MaxMaxURLLength = 8192
)

var (
	ErrEmptyURL          = errors.New("url is empty")
	ErrURLTooLong        = errors.New("url exceeds maximum length")
	ErrInvalidCharacters = errors.New("url contains invalid or control characters")
	ErrUnsupportedScheme = errors.New("url scheme must be http or https")
	ErrCredentials       = errors.New("url must not contain credentials")
	ErrInvalidHost       = errors.New("url host is missing or invalid")
	ErrInvalidPort       = errors.New("url port is invalid")
	ErrInvalidURL        = errors.New("url is malformed")
)

// ValidateAndNormalize validates rawURL against strict security boundaries and
// returns a canonicalized URL string. It accepts only absolute http and https
// URLs without userinfo, control characters, backslashes in the authority/path,
// or malformed ports.
func ValidateAndNormalize(rawURL string, maxLen int) (string, error) {
	if maxLen <= 0 {
		maxLen = DefaultMaxURLLength
	}
	if rawURL == "" {
		return "", ErrEmptyURL
	}
	if len(rawURL) > maxLen {
		return "", ErrURLTooLong
	}

	// Reject any control characters, whitespace, or non-ASCII bytes to prevent
	// header/log injection and parser differentials across HTTP clients/browsers.
	for i := 0; i < len(rawURL); i++ {
		b := rawURL[i]
		if b < 0x21 || b > 0x7E {
			return "", ErrInvalidCharacters
		}
	}

	// Reject backslashes before query/fragment: browsers (WHATWG URL) treat '\'
	// as '/' in special schemes, whereas Go's net/url may treat it differently.
	authorityAndPath := rawURL
	if idx := strings.IndexAny(authorityAndPath, "?#"); idx != -1 {
		authorityAndPath = authorityAndPath[:idx]
	}
	if strings.ContainsRune(authorityAndPath, '\\') {
		return "", ErrInvalidCharacters
	}

	// Must have an explicit "://" after scheme (rejects "http:example.com",
	// "//example.com", "/relative", "javascript:alert(1)", etc.).
	schemeEnd := strings.Index(rawURL, "://")
	if schemeEnd <= 0 {
		return "", ErrUnsupportedScheme
	}
	scheme := strings.ToLower(rawURL[:schemeEnd])
	if scheme != "http" && scheme != "https" {
		return "", ErrUnsupportedScheme
	}

	// Check raw authority for '@' (catches "https://@evil.com" where u.User may
	// be empty or ambiguous).
	rest := rawURL[schemeEnd+3:]
	rawAuthority := rest
	if idx := strings.IndexAny(rawAuthority, "/?#"); idx != -1 {
		rawAuthority = rawAuthority[:idx]
	}
	if rawAuthority == "" {
		return "", ErrInvalidHost
	}
	if strings.ContainsRune(rawAuthority, '@') {
		return "", ErrCredentials
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", ErrInvalidURL
	}
	if parsed.User != nil {
		return "", ErrCredentials
	}

	parsedScheme := strings.ToLower(parsed.Scheme)
	if parsedScheme != "http" && parsedScheme != "https" {
		return "", ErrUnsupportedScheme
	}

	host, port, err := splitAndValidateHostPort(parsed.Host, rawAuthority)
	if err != nil {
		return "", err
	}

	// Canonicalize: lowercase scheme and host, omit default port.
	parsed.Scheme = parsedScheme
	if port == "" || (parsedScheme == "http" && port == "80") || (parsedScheme == "https" && port == "443") {
		if strings.Contains(host, ":") {
			parsed.Host = "[" + host + "]"
		} else {
			parsed.Host = host
		}
	} else {
		if strings.Contains(host, ":") {
			parsed.Host = "[" + host + "]:" + port
		} else {
			parsed.Host = host + ":" + port
		}
	}

	canonical := parsed.String()
	if len(canonical) > maxLen {
		return "", ErrURLTooLong
	}
	return canonical, nil
}

func splitAndValidateHostPort(parsedHost, rawAuthority string) (string, string, error) {
	if parsedHost == "" || parsedHost != rawAuthority {
		return "", "", ErrInvalidHost
	}

	var host, port string
	if strings.HasPrefix(parsedHost, "[") {
		// IPv6 literal: must be [addr] or [addr]:port
		closeIdx := strings.IndexRune(parsedHost, ']')
		if closeIdx <= 1 {
			return "", "", ErrInvalidHost
		}
		host = parsedHost[1:closeIdx]
		after := parsedHost[closeIdx+1:]
		if after != "" {
			if !strings.HasPrefix(after, ":") {
				return "", "", ErrInvalidHost
			}
			port = after[1:]
			if port == "" {
				return "", "", ErrInvalidPort
			}
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() != nil {
			return "", "", ErrInvalidHost
		}
		host = strings.ToLower(ip.String())
	} else {
		// IPv4 or hostname, optionally with :port
		if strings.ContainsAny(parsedHost, "[]") {
			return "", "", ErrInvalidHost
		}
		colonIdx := strings.IndexRune(parsedHost, ':')
		if colonIdx != -1 {
			// Multiple colons without brackets is invalid.
			if strings.LastIndexByte(parsedHost, ':') != colonIdx {
				return "", "", ErrInvalidPort
			}
			host = parsedHost[:colonIdx]
			port = parsedHost[colonIdx+1:]
			if port == "" {
				return "", "", ErrInvalidPort
			}
		} else {
			host = parsedHost
		}

		if host == "" {
			return "", "", ErrInvalidHost
		}
		host = strings.ToLower(host)
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() == nil {
				return "", "", ErrInvalidHost
			}
			host = ip.String()
		} else if !isValidHostname(host) {
			return "", "", ErrInvalidHost
		}
	}

	if port != "" {
		if !isValidPort(port) {
			return "", "", ErrInvalidPort
		}
	}
	return host, port, nil
}

func isValidPort(port string) bool {
	if len(port) == 0 || len(port) > 5 {
		return false
	}
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	return true
}

func isValidHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	// Reject trailing or leading dots to avoid ambiguous FQDN vs relative names.
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	// Reject all-numeric TLD (e.g., "123" or "example.123" when not a valid IPv4).
	last := labels[len(labels)-1]
	allDigits := true
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			allDigits = false
			break
		}
	}
	return !allDigits
}
