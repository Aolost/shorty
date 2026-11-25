package config

import (
	"testing"
	"time"
)

func TestParse_PrivateRequiresToken(t *testing.T) {
	_, err := Parse(nil, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when private mode has no bearer token")
	}

	_, err = Parse([]string{"-token", "short"}, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when bearer token is too short")
	}

	cfg, err := Parse([]string{"-token", "0123456789abcdef"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != ModePrivate {
		t.Fatalf("got mode %q, want %q", cfg.Mode, ModePrivate)
	}
}

func TestParse_PublicModeExplicitAndEnvOverrides(t *testing.T) {
	env := map[string]string{
		"SHORTY_MODE":            "public",
		"SHORTY_REDIRECT_STATUS": "307",
		"SHORTY_INTERSTITIAL":    "true",
		"SHORTY_RETENTION":       "24h",
	}
	cfg, err := Parse([]string{"-code-length", "12"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != ModePublic || cfg.RedirectStatus != 307 || !cfg.Interstitial || cfg.Retention != 24*time.Hour || cfg.CodeLength != 12 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParse_InvalidBounds(t *testing.T) {
	cases := [][]string{
		{"-mode", "open"},
		{"-mode", "public", "-redirect-status", "301"},
		{"-mode", "public", "-max-url-length", "10"},
		{"-mode", "public", "-code-length", "4"},
		{"-mode", "public", "-rate-limit-rps", "0"},
		{"-mode", "public", "-max-body-bytes", "999999"},
		{"-mode", "public", "-request-timeout", "10ms"},
		{"-mode", "public", "-retention", "-1s"},
		{"-mode", "public", "-base-url", "javascript:alert(1)"},
	}
	for _, args := range cases {
		if _, err := Parse(args, func(string) string { return "" }); err == nil {
			t.Errorf("expected error for args %v", args)
		}
	}
}
