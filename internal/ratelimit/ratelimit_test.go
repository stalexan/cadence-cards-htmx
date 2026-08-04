package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func TestGeneralIPLimit(t *testing.T) {
	l := New(false)
	for i := 0; i < 1000; i++ {
		if l.CheckIPRateLimit("1.2.3.4", t0) {
			t.Fatalf("limited at request %d", i+1)
		}
	}
	if !l.CheckIPRateLimit("1.2.3.4", t0) {
		t.Error("request 1001 should be limited")
	}
	// Other IPs unaffected.
	if l.CheckIPRateLimit("5.6.7.8", t0) {
		t.Error("other IP should not be limited")
	}
	// Window reset clears the counter.
	if l.CheckIPRateLimit("1.2.3.4", t0.Add(16*time.Minute)) {
		t.Error("after window reset should not be limited")
	}
}

func TestAccountLockoutAfterThreeFailures(t *testing.T) {
	l := New(false)
	for i := 0; i < 2; i++ {
		l.RecordFailedAuth("1.2.3.4", "a@b.c", t0)
		if l.IsAccountLockedOut("a@b.c", t0) {
			t.Fatalf("locked after %d failures", i+1)
		}
	}
	l.RecordFailedAuth("1.2.3.4", "a@b.c", t0)
	if !l.IsAccountLockedOut("a@b.c", t0) {
		t.Error("account should lock after 3 failures")
	}
	// Lockout expires after 30 minutes.
	if l.IsAccountLockedOut("a@b.c", t0.Add(31*time.Minute)) {
		t.Error("lockout should expire")
	}
}

func TestIPCooldownAndLockout(t *testing.T) {
	l := New(false)
	// Use distinct emails so only the IP path trips.
	for i := 0; i < 3; i++ {
		l.RecordFailedAuth("9.9.9.9", fmt.Sprintf("u%d@x.y", i), t0)
	}
	if !l.IsIPLockedOut("9.9.9.9", t0) {
		t.Error("IP should cool down after 3 failures")
	}
	// Cooldown is 5 minutes.
	if l.IsIPLockedOut("9.9.9.9", t0.Add(6*time.Minute)) {
		t.Error("cooldown should expire after 5 minutes")
	}
	// Two more failures -> 15-minute lockout.
	l.RecordFailedAuth("9.9.9.9", "u3@x.y", t0.Add(6*time.Minute))
	l.RecordFailedAuth("9.9.9.9", "u4@x.y", t0.Add(6*time.Minute))
	if !l.IsIPLockedOut("9.9.9.9", t0.Add(6*time.Minute)) {
		t.Error("IP should lock after 5 failures")
	}
	if l.IsIPLockedOut("9.9.9.9", t0.Add(6*time.Minute).Add(16*time.Minute)) {
		t.Error("lockout should expire after 15 minutes")
	}
}

func TestClearFailedAttempts(t *testing.T) {
	l := New(false)
	for i := 0; i < 3; i++ {
		l.RecordFailedAuth("1.1.1.1", "a@b.c", t0)
	}
	l.ClearFailedAttempts("1.1.1.1", "a@b.c")
	if l.IsIPLockedOut("1.1.1.1", t0) || l.IsAccountLockedOut("a@b.c", t0) {
		t.Error("successful login should clear lockouts")
	}
}

func TestDisabled(t *testing.T) {
	l := New(true)
	for i := 0; i < 5; i++ {
		l.RecordFailedAuth("1.1.1.1", "a@b.c", t0)
	}
	if l.CheckIPRateLimit("1.1.1.1", t0) || l.IsIPLockedOut("1.1.1.1", t0) || l.IsAccountLockedOut("a@b.c", t0) {
		t.Error("disabled limiter should never limit")
	}
}
