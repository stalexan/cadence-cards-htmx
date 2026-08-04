// Package ratelimit ports web/src/lib/server/rate-limiter.ts: in-memory
// per-IP request limiting plus progressive auth-failure lockouts.
package ratelimit

import (
	"sync"
	"time"
)

// Settings mirror RATE_LIMIT_SETTINGS in the source.
const (
	// General IP rate limiting.
	defaultWindow      = 15 * time.Minute
	defaultMaxRequests = 1000

	// Authentication attempt rate limiting.
	authAttemptWindow   = 30 * time.Minute
	authAttemptMaxTries = 3

	// IP lockouts: 3 failures = 5-minute cooldown, 5 failures = 15-minute lockout.
	ipCooldownThreshold = 3
	ipLockoutThreshold  = 5
	ipCooldownDuration  = 5 * time.Minute
	ipLockoutDuration   = 15 * time.Minute

	// Account (email) lockout: 3 failures in 30 minutes = 30-minute lockout.
	accountFailureWindow    = 30 * time.Minute
	accountLockoutThreshold = 3
	accountLockoutDuration  = 30 * time.Minute

	// Cleanup.
	CleanupInterval    = time.Hour
	cleanupEntryExpiry = time.Hour
)

type rateLimitCount struct {
	count     int
	resetTime time.Time
}

type failedAuthCount struct {
	attempts     []time.Time
	lockoutUntil time.Time // zero = not locked
}

// Limiter is the in-memory rate limiter. Disabled short-circuits all checks
// (DISABLE_RATE_LIMITING dev bypass).
type Limiter struct {
	Disabled bool

	mu                sync.Mutex
	ipRateLimits      map[string]*rateLimitCount
	ipAuthFailures    map[string]*failedAuthCount
	emailAuthFailures map[string]*failedAuthCount
}

// New creates a Limiter.
func New(disabled bool) *Limiter {
	return &Limiter{
		Disabled:          disabled,
		ipRateLimits:      map[string]*rateLimitCount{},
		ipAuthFailures:    map[string]*failedAuthCount{},
		emailAuthFailures: map[string]*failedAuthCount{},
	}
}

// CheckIPRateLimit records one request and reports whether the IP is over the
// general limit (1000 requests / 15 minutes).
func (l *Limiter) CheckIPRateLimit(ip string, now time.Time) bool {
	if l.Disabled {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.ipRateLimits[ip]
	if !ok || now.After(entry.resetTime) {
		l.ipRateLimits[ip] = &rateLimitCount{count: 1, resetTime: now.Add(defaultWindow)}
		return false
	}
	entry.count++
	return entry.count > defaultMaxRequests
}

func pruneAttempts(attempts []time.Time, now time.Time, window time.Duration) []time.Time {
	kept := attempts[:0]
	for _, at := range attempts {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	return kept
}

// CheckAuthAttemptRateLimit reports whether the IP has too many recent failed
// auth attempts (3 in 30 minutes) without recording anything.
func (l *Limiter) CheckAuthAttemptRateLimit(ip string, now time.Time) bool {
	if l.Disabled {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.ipAuthFailures[ip]
	if !ok {
		return false
	}
	entry.attempts = pruneAttempts(entry.attempts, now, authAttemptWindow)
	return len(entry.attempts) >= authAttemptMaxTries
}

// RecordFailedAuth registers one failed login for the IP (and email when
// given), applying progressive lockouts.
func (l *Limiter) RecordFailedAuth(ip, email string, now time.Time) {
	if l.Disabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	ipEntry, ok := l.ipAuthFailures[ip]
	if !ok {
		ipEntry = &failedAuthCount{}
		l.ipAuthFailures[ip] = ipEntry
	}
	ipEntry.attempts = pruneAttempts(ipEntry.attempts, now, authAttemptWindow)
	ipEntry.attempts = append(ipEntry.attempts, now)
	switch {
	case len(ipEntry.attempts) >= ipLockoutThreshold:
		ipEntry.lockoutUntil = now.Add(ipLockoutDuration)
	case len(ipEntry.attempts) >= ipCooldownThreshold:
		ipEntry.lockoutUntil = now.Add(ipCooldownDuration)
	}

	if email != "" {
		emailEntry, ok := l.emailAuthFailures[email]
		if !ok {
			emailEntry = &failedAuthCount{}
			l.emailAuthFailures[email] = emailEntry
		}
		emailEntry.attempts = pruneAttempts(emailEntry.attempts, now, accountFailureWindow)
		emailEntry.attempts = append(emailEntry.attempts, now)
		if len(emailEntry.attempts) >= accountLockoutThreshold {
			emailEntry.lockoutUntil = now.Add(accountLockoutDuration)
		}
	}
}

// IsIPLockedOut reports whether the IP is under an auth lockout.
func (l *Limiter) IsIPLockedOut(ip string, now time.Time) bool {
	if l.Disabled {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.ipAuthFailures[ip]
	if !ok || entry.lockoutUntil.IsZero() {
		return false
	}
	if now.After(entry.lockoutUntil) {
		entry.lockoutUntil = time.Time{}
		return false
	}
	return true
}

// IsAccountLockedOut reports whether the email is under an account lockout.
func (l *Limiter) IsAccountLockedOut(email string, now time.Time) bool {
	if l.Disabled {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.emailAuthFailures[email]
	if !ok || entry.lockoutUntil.IsZero() {
		return false
	}
	if now.After(entry.lockoutUntil) {
		entry.lockoutUntil = time.Time{}
		return false
	}
	return true
}

// ClearFailedAttempts resets failure tracking after a successful login.
func (l *Limiter) ClearFailedAttempts(ip, email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ipAuthFailures, ip)
	if email != "" {
		delete(l.emailAuthFailures, email)
	}
}

// Cleanup drops expired entries (run hourly).
func (l *Limiter) Cleanup(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for ip, entry := range l.ipRateLimits {
		if now.After(entry.resetTime) {
			delete(l.ipRateLimits, ip)
		}
	}
	stale := func(entry *failedAuthCount) bool {
		if len(entry.attempts) == 0 {
			return true
		}
		last := entry.attempts[len(entry.attempts)-1]
		return now.Sub(last) > cleanupEntryExpiry
	}
	for ip, entry := range l.ipAuthFailures {
		if stale(entry) {
			delete(l.ipAuthFailures, ip)
		}
	}
	for email, entry := range l.emailAuthFailures {
		if stale(entry) {
			delete(l.emailAuthFailures, email)
		}
	}
}
