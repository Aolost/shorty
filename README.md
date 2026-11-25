# shorty

`shorty` is an intentionally small, self-hosted URL shortener in Go (1.22+) with SQLite storage and explicit security boundaries.

> **Important:** Shortening a URL does **not** make the destination trustworthy, safe, or verified. Operators and users should treat shortened links with the same caution as any unverified external link.

## Threat & Abuse Model

### Assets
- Users' trust (not being tricked into `javascript:`/`data:` execution or open-redirect laundering).
- Service availability (bounded bodies, timeouts, per-IP rate limiting, bounded limiter memory).
- Stored destinations and operator credentials.

### Controls & Boundaries
- **Strict URL validation & single normalization:** Only absolute `http` and `https` URLs with a valid host are accepted. Credentials (`user:pass@`), ASCII control characters, whitespace, non-ASCII characters, backslashes in authority/path, unsupported schemes (`javascript:`, `data:`, `file:`, `ftp:`, scheme-relative `//...`), malformed ports, and oversized URLs are rejected. Destinations are normalized once (lowercase scheme/host, default ports stripped) before storage.
- **No open redirects:** `GET /{code}` only redirects (`302` or `307`) to a stored, re-validated destination. Query parameters on redirect requests are never used as redirect targets.
- **Opaque random codes:** Codes are generated using `crypto/rand` over a 64-character URL-safe alphabet (default 11 chars = 66 bits of entropy) and are never derived from the destination URL.
- **Explicit auth modes:** Defaults to `private` mode, which requires an operator-configured bearer token (`SHORTY_TOKEN` / `-token`, minimum 16 characters; no default credential is shipped). `public` mode is disabled unless explicitly enabled (`-mode=public`).
- **Operational limits:** Creation is rate-limited per IP (token bucket), request bodies are bounded via `http.MaxBytesReader`, HTTP server and per-request timeouts are enforced, and error responses are generic without internal details.
- **No outbound fetches:** `shorty` never fetches, previews, or crawls submitted destination URLs.
- **Optional interstitial page:** When `-interstitial` / `SHORTY_INTERSTITIAL=true` is enabled, `GET /{code}` displays the destination host and URL with a warning before the user clicks through (`rel="noopener noreferrer"`, strict CSP).

### Out of Scope
- Anonymity, large-scale public multi-tenant hosting, automated phishing classification, and protection against a compromised host or database.

## Privacy & Logging Behavior
- **Structured redacted logs:** JSON logs record `method`, `path` (never query strings), `status`, `duration_ms`, and `remote_ip`. `Authorization` headers and destination query strings are never logged.
- **Referrer & cache headers:** Redirects and interstitials set `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, and `Cache-Control: no-store`.
- **Health endpoint:** `GET /healthz` returns `{"status":"ok"}` (or `503` if SQLite is unreachable) with no version, path, or link count disclosure.
- **No analytics or third-party calls.**

## Deployment Requirements
- Run `shorty` behind a TLS-terminating reverse proxy (see [deploy/Caddyfile](deploy/Caddyfile) and [deploy/nginx.conf](deploy/nginx.conf)), bound to loopback (`127.0.0.1:8080`).
- Only enable `-trust-proxy=true` when `shorty` is reachable exclusively through your trusted reverse proxy that overwrites `X-Forwarded-For`.
- In `private` mode, supply a strong random bearer token via `SHORTY_TOKEN` (at least 16 characters).

## Quick Start

```bash
export SHORTY_TOKEN="$(openssl rand -hex 24)"
make build
./shorty -base-url https://s.example.com
```

### API

Create a short link:
```bash
curl -sS -X POST http://127.0.0.1:8080/api/v1/links \
  -H "Authorization: Bearer $SHORTY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/docs"}'
```

Resolve a short link:
```bash
curl -i http://127.0.0.1:8080/<code>
```

Health check:
```bash
curl -sS http://127.0.0.1:8080/healthz
```

## Deletion & Retention

### HTTP API Deletion
Requires the operator bearer token (`SHORTY_TOKEN` / `-token`):
```bash
curl -i -X DELETE http://127.0.0.1:8080/api/v1/links/<code> \
  -H "Authorization: Bearer $SHORTY_TOKEN"
```

### CLI Deletion & Pruning
Delete a specific code directly from SQLite:
```bash
./shorty delete -db shorty.db <code>
```

Prune expired links manually:
```bash
./shorty prune -db shorty.db
```

### Automatic Retention
Set `-retention 720h` (or `SHORTY_RETENTION=720h`) to expire links automatically after 30 days. Expired links are rejected on lookup and periodically pruned in the background.

## Development & Verification

```bash
make check   # gofmt, go vet, go test -race, and build
make fuzz    # run URL validation and routing fuzzers
```
