package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shorty/shorty/internal/shortcode"
	"github.com/shorty/shorty/internal/urlval"
)

const (
	ModePrivate = "private"
	ModePublic  = "public"

	MinTokenLength = 16
)

// Config holds validated runtime configuration for shorty.
type Config struct {
	ListenAddr     string
	BaseURL        string
	DBPath         string
	Mode           string
	BearerToken    string
	RedirectStatus int
	Interstitial   bool
	MaxURLLength   int
	CodeLength     int
	RateLimitRPS   float64
	RateLimitBurst int
	MaxBodyBytes   int64
	RequestTimeout time.Duration
	Retention      time.Duration
	TrustProxy     bool
}

// Default returns a Config populated with safe defaults.
// Note: BearerToken has no default and must be provided by the operator in private mode.
func Default() Config {
	return Config{
		ListenAddr:     "127.0.0.1:8080",
		BaseURL:        "",
		DBPath:         "shorty.db",
		Mode:           ModePrivate,
		BearerToken:    "",
		RedirectStatus: 302,
		Interstitial:   false,
		MaxURLLength:   urlval.DefaultMaxURLLength,
		CodeLength:     shortcode.DefaultLength,
		RateLimitRPS:   2.0,
		RateLimitBurst: 10,
		MaxBodyBytes:   4096,
		RequestTimeout: 5 * time.Second,
		Retention:      0,
		TrustProxy:     false,
	}
}

// Parse parses configuration from environment variables and CLI flags, then validates bounds.
func Parse(args []string, getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Default()

	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}

	fs := flag.NewFlagSet("shorty", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP listen address")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "External base URL for short links")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite database file path")
	fs.StringVar(&cfg.Mode, "mode", cfg.Mode, "Access mode: private or public")
	fs.StringVar(&cfg.BearerToken, "token", cfg.BearerToken, "Operator bearer token (required in private mode)")
	fs.IntVar(&cfg.RedirectStatus, "redirect-status", cfg.RedirectStatus, "HTTP redirect status (302 or 307)")
	fs.BoolVar(&cfg.Interstitial, "interstitial", cfg.Interstitial, "Show interstitial confirmation page before redirect")
	fs.IntVar(&cfg.MaxURLLength, "max-url-length", cfg.MaxURLLength, "Maximum destination URL length")
	fs.IntVar(&cfg.CodeLength, "code-length", cfg.CodeLength, "Generated short code length")
	fs.Float64Var(&cfg.RateLimitRPS, "rate-limit-rps", cfg.RateLimitRPS, "Creation rate limit (requests/sec per IP)")
	fs.IntVar(&cfg.RateLimitBurst, "rate-limit-burst", cfg.RateLimitBurst, "Creation rate limit burst")
	fs.Int64Var(&cfg.MaxBodyBytes, "max-body-bytes", cfg.MaxBodyBytes, "Maximum request body bytes")
	fs.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "Per-request handler timeout")
	fs.DurationVar(&cfg.Retention, "retention", cfg.Retention, "Link retention duration (0 disables expiry)")
	fs.BoolVar(&cfg.TrustProxy, "trust-proxy", cfg.TrustProxy, "Trust X-Forwarded-For from reverse proxy")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	if v := getenv("SHORTY_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := getenv("SHORTY_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := getenv("SHORTY_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := getenv("SHORTY_MODE"); v != "" {
		cfg.Mode = v
	}
	if v := getenv("SHORTY_TOKEN"); v != "" {
		cfg.BearerToken = v
	}
	if v := getenv("SHORTY_REDIRECT_STATUS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_REDIRECT_STATUS: %w", err)
		}
		cfg.RedirectStatus = n
	}
	if v := getenv("SHORTY_INTERSTITIAL"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_INTERSTITIAL: %w", err)
		}
		cfg.Interstitial = b
	}
	if v := getenv("SHORTY_MAX_URL_LENGTH"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_MAX_URL_LENGTH: %w", err)
		}
		cfg.MaxURLLength = n
	}
	if v := getenv("SHORTY_CODE_LENGTH"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_CODE_LENGTH: %w", err)
		}
		cfg.CodeLength = n
	}
	if v := getenv("SHORTY_RATE_LIMIT_RPS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_RATE_LIMIT_RPS: %w", err)
		}
		cfg.RateLimitRPS = f
	}
	if v := getenv("SHORTY_RATE_LIMIT_BURST"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_RATE_LIMIT_BURST: %w", err)
		}
		cfg.RateLimitBurst = n
	}
	if v := getenv("SHORTY_MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_MAX_BODY_BYTES: %w", err)
		}
		cfg.MaxBodyBytes = n
	}
	if v := getenv("SHORTY_REQUEST_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_REQUEST_TIMEOUT: %w", err)
		}
		cfg.RequestTimeout = d
	}
	if v := getenv("SHORTY_RETENTION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_RETENTION: %w", err)
		}
		cfg.Retention = d
	}
	if v := getenv("SHORTY_TRUST_PROXY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid SHORTY_TRUST_PROXY: %w", err)
		}
		cfg.TrustProxy = b
	}
	return nil
}

// Validate checks all configuration invariants and operational bounds.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.ListenAddr) == "" {
		return errors.New("listen address must not be empty")
	}
	if strings.TrimSpace(c.DBPath) == "" {
		return errors.New("database path must not be empty")
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode != ModePrivate && c.Mode != ModePublic {
		return fmt.Errorf("mode must be %q or %q", ModePrivate, ModePublic)
	}
	if c.Mode == ModePrivate {
		if len(c.BearerToken) < MinTokenLength {
			return fmt.Errorf("private mode requires a bearer token of at least %d characters (no default credential is shipped)", MinTokenLength)
		}
	} else if c.BearerToken != "" && len(c.BearerToken) < MinTokenLength {
		return fmt.Errorf("bearer token, when set, must be at least %d characters", MinTokenLength)
	}

	if c.BaseURL != "" {
		norm, err := urlval.ValidateAndNormalize(strings.TrimRight(c.BaseURL, "/"), urlval.DefaultMaxURLLength)
		if err != nil {
			return fmt.Errorf("invalid base-url: %w", err)
		}
		c.BaseURL = strings.TrimRight(norm, "/")
	}

	if c.RedirectStatus != 302 && c.RedirectStatus != 307 {
		return errors.New("redirect-status must be 302 or 307")
	}
	if c.MaxURLLength < urlval.MinMaxURLLength || c.MaxURLLength > urlval.MaxMaxURLLength {
		return fmt.Errorf("max-url-length must be between %d and %d", urlval.MinMaxURLLength, urlval.MaxMaxURLLength)
	}
	if c.CodeLength < shortcode.MinLength || c.CodeLength > shortcode.MaxLength {
		return fmt.Errorf("code-length must be between %d and %d", shortcode.MinLength, shortcode.MaxLength)
	}
	if c.RateLimitRPS < 0.1 || c.RateLimitRPS > 1000 {
		return errors.New("rate-limit-rps must be between 0.1 and 1000")
	}
	if c.RateLimitBurst < 1 || c.RateLimitBurst > 10000 {
		return errors.New("rate-limit-burst must be between 1 and 10000")
	}
	if c.MaxBodyBytes < 256 || c.MaxBodyBytes > 65536 {
		return errors.New("max-body-bytes must be between 256 and 65536")
	}
	if c.RequestTimeout < 100*time.Millisecond || c.RequestTimeout > 60*time.Second {
		return errors.New("request-timeout must be between 100ms and 60s")
	}
	if c.Retention < 0 {
		return errors.New("retention must not be negative")
	}
	if c.Retention > 0 && c.Retention < time.Second {
		return errors.New("retention, when enabled, must be at least 1s")
	}
	return nil
}
