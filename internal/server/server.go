package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/shorty/shorty/internal/config"
	"github.com/shorty/shorty/internal/ratelimit"
	"github.com/shorty/shorty/internal/shortcode"
	"github.com/shorty/shorty/internal/store"
	"github.com/shorty/shorty/internal/urlval"
)

var interstitialTmpl = template.Must(template.New("interstitial").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Redirect Notice</title>
<style>
body{font-family:system-ui,-apple-system,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem;line-height:1.5;color:#111}
.card{border:1px solid #d0d7de;border-radius:8px;padding:1.5rem;background:#f6f8fa}
.host{font-weight:700;word-break:break-all}
.dest{font-family:monospace;font-size:0.9rem;word-break:break-all;background:#fff;padding:0.75rem;border:1px solid #d0d7de;border-radius:4px}
.btn{display:inline-block;margin-top:1rem;padding:0.6rem 1.1rem;background:#0969da;color:#fff;text-decoration:none;border-radius:6px;font-weight:600}
.warn{font-size:0.9rem;color:#57606a}
</style>
</head>
<body>
<div class="card">
  <h1>External Redirect Notice</h1>
  <p>You are about to leave this service for host: <span class="host">{{.Host}}</span></p>
  <p class="dest">{{.Destination}}</p>
  <p class="warn">Shortening a URL does not verify or endorse the destination. Proceed only if you trust this destination.</p>
  <a class="btn" href="{{.Destination}}" rel="noopener noreferrer">Continue to {{.Host}}</a>
</div>
</body>
</html>
`))

// Server implements the HTTP API and redirect endpoints for shorty.
type Server struct {
	cfg     config.Config
	store   *store.Store
	limiter *ratelimit.Limiter
	logger  *slog.Logger
	mux     *http.ServeMux
}

// New constructs a Server with all routes and security middleware wired.
func New(cfg config.Config, st *store.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	s := &Server{
		cfg:     cfg,
		store:   st,
		limiter: ratelimit.New(cfg.RateLimitRPS, cfg.RateLimitBurst),
		logger:  logger,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("POST /api/v1/links", s.handleCreateLink)
	s.mux.HandleFunc("DELETE /api/v1/links/{code}", s.handleDeleteLink)
	s.mux.HandleFunc("GET /{code}", s.handleResolve)
}

// Handler returns the root http.Handler wrapped with logging, path hygiene, and timeout middleware.
func (s *Server) Handler() http.Handler {
	return s.loggingMiddleware(s.timeoutMiddleware(s.pathHygieneMiddleware(s.mux)))
}

func (s *Server) pathHygieneMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Clean(r.URL.Path) != r.URL.Path || strings.ContainsRune(r.URL.Path, '\\') {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) timeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Redacted structured log: never log Authorization headers or query strings.
		s.logger.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_ip", s.clientIP(r),
		)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createRequest struct {
	URL string `json:"url"`
}

type createResponse struct {
	Code        string `json:"code"`
	ShortURL    string `json:"short_url"`
	Destination string `json:"destination"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if s.cfg.Mode == config.ModePrivate {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	if !s.limiter.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req createRequest
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Reject trailing JSON tokens.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	canonical, err := urlval.ValidateAndNormalize(req.URL, s.cfg.MaxURLLength)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination url")
		return
	}

	link, err := s.store.Create(r.Context(), canonical)
	if err != nil {
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "request timed out")
			return
		}
		s.logger.Error("create_link_failed", "reason", "storage_error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	destHost := ""
	if parsed, err := url.Parse(canonical); err == nil {
		destHost = parsed.Host
	}
	s.logger.Info("link_created", "code", link.Code, "dest_host", destHost)

	shortURL := "/" + link.Code
	if s.cfg.BaseURL != "" {
		shortURL = s.cfg.BaseURL + "/" + link.Code
	}

	resp := createResponse{
		Code:        link.Code,
		ShortURL:    shortURL,
		Destination: link.Destination,
	}
	if !link.ExpiresAt.IsZero() {
		resp.ExpiresAt = link.ExpiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if s.cfg.BearerToken == "" {
		writeError(w, http.StatusForbidden, "deletion requires operator token")
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	code := r.PathValue("code")
	if !shortcode.IsValid(code) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	err := s.store.Delete(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.logger.Error("delete_link_failed", "code", code, "reason", "storage_error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	s.logger.Info("link_deleted", "code", code)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")

	code := r.PathValue("code")
	if !shortcode.IsValid(code) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	link, err := s.store.Resolve(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "request timed out")
			return
		}
		s.logger.Error("resolve_link_failed", "code", code, "reason", "storage_error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Defense in depth: re-validate stored destination before redirecting.
	canonical, err := urlval.ValidateAndNormalize(link.Destination, s.cfg.MaxURLLength)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if s.cfg.Interstitial {
		parsed, err := url.Parse(canonical)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
		_ = interstitialTmpl.Execute(w, struct {
			Host        string
			Destination string
		}{
			Host:        parsed.Host,
			Destination: canonical,
		})
		return
	}

	http.Redirect(w, r, canonical, s.cfg.RedirectStatus)
}

func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.BearerToken == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	provided := auth[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.BearerToken)) == 1
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				candidate := strings.TrimSpace(parts[0])
				if ip := net.ParseIP(candidate); ip != nil {
					return ip.String()
				}
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
