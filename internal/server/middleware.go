package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// middleware chain (outermost first): request logging → client IP → general
// rate limit → security headers → session load. Per-route: requireUser and
// the POST origin check. Mirrors the hooks.server.ts sequence.

func requestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// logRequests emits one structured line per request.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = requestID()
		}
		lw := &loggingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)
		slog.Info("request",
			"requestId", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", lw.status,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}

type loggingWriter struct {
	http.ResponseWriter
	status int
}

func (w *loggingWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// resolveClientIP trusts only the rightmost X-Forwarded-For entry — the one the
// proxy in front of the app wrote — falling back to the socket address.
// `tailscale serve` replaces the header with the tailnet peer's address; an
// appending proxy (nginx's $proxy_add_x_forwarded_for) puts the address it saw
// last. Entries to its left, and X-Real-IP, which tailscale serve passes
// through untouched, are client-supplied and spoofable.
func resolveClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := forwardedFor(r.Header.Values("X-Forwarded-For"))
		if ip == "" {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			ip = host
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClientIP, ip)))
	})
}

// forwardedFor returns the last address across all X-Forwarded-For lines, or
// "" if it is absent or not an IP.
func forwardedFor(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	last := lines[len(lines)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(last))
	if err != nil {
		return ""
	}
	return addr.Unmap().String()
}

// rateLimit applies the general per-IP request limit.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter.CheckIPRateLimit(clientIP(r), time.Now()) {
			slog.Warn("rate limit exceeded", "ip", clientIP(r), "path", r.URL.Path)
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets the response headers from hooks.server.ts plus a CSP
// stricter than the source: no inline scripts or styles exist, so no nonces
// or 'unsafe-inline' are needed. htmx runs with allowEval:false (see
// base.html), so 'unsafe-eval' is not required either.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
		"frame-ancestors 'none'; form-action 'self'; base-uri 'self'; object-src 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// checkOrigin rejects cross-origin non-GET requests (CSRF insurance on top of
// SameSite=Lax cookies). Same-origin browsers send Sec-Fetch-Site:
// same-origin; older clients send Origin/Referer matching the Host.
func checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			if site == "same-origin" || site == "none" {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "Cross-origin request rejected", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "Cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
