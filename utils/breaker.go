package utils

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// State is the circuit breaker's current position.
type State string

const (
	StateClosed   State = "closed"    // normal — calls pass through
	StateOpen     State = "open"      // upstream known-dead — reject immediately
	StateHalfOpen State = "half-open" // probing — allow exactly one call
)

// Breaker guards an upstream dependency that charges money per call.
//
// The production log shows 38 requests billed against a Gemini account whose
// credits were already exhausted, spread over six days. Every one of those
// was a paid call with a guaranteed-failing outcome. Once two consecutive
// quota errors are seen the breaker opens and subsequent try-ons are rejected
// locally, with no upstream request and no charge, until a probe succeeds.
type Breaker struct {
	name string

	// Trip thresholds.
	quotaThreshold   int           // consecutive quota errors before opening
	failureThreshold int           // consecutive generic errors before opening
	openFor          time.Duration // how long to stay open before probing
	reopenFor        time.Duration // how long to stay open after a failed probe

	mu               sync.Mutex
	state            State
	consecutiveQuota int
	consecutiveFail  int
	openedAt         time.Time
	openUntil        time.Time
	probeInFlight    bool
	probeStartedAt   time.Time
	lastReason       string
	tripped          int

	now func() time.Time // injectable clock for tests
}

// probeTimeout bounds how long a half-open probe may stay unreported before
// another caller is allowed to try.
const probeTimeout = 2 * time.Minute

// GeminiBreaker guards the image-generation upstream.
var GeminiBreaker = NewBreaker("gemini")

// NewBreaker builds a breaker with the defaults from the fix plan: open after
// 2 consecutive quota errors, probe after 5 minutes, and re-open for 10
// minutes if the probe fails.
func NewBreaker(name string) *Breaker {
	return &Breaker{
		name:             name,
		quotaThreshold:   2,
		failureThreshold: 5,
		openFor:          5 * time.Minute,
		reopenFor:        10 * time.Minute,
		state:            StateClosed,
		now:              time.Now,
	}
}

// Allow reports whether a call may proceed. When it returns false the caller
// must fail fast without touching the upstream.
//
// In the half-open state exactly one caller is admitted as a probe; everyone
// else is still rejected until that probe reports back.
func (b *Breaker) Allow() (bool, State) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true, StateClosed

	case StateOpen:
		if b.now().Before(b.openUntil) {
			return false, StateOpen
		}
		b.state = StateHalfOpen
		b.probeInFlight = true
		b.probeStartedAt = b.now()
		slog.Warn("circuit breaker half-open, sending probe", "breaker", b.name)
		return true, StateHalfOpen

	case StateHalfOpen:
		// A probe that never reported back (the caller bailed before
		// reaching the upstream) must not wedge the breaker shut forever, so
		// an in-flight probe is considered abandoned after probeTimeout.
		if b.probeInFlight && b.now().Sub(b.probeStartedAt) < probeTimeout {
			return false, StateHalfOpen
		}
		b.probeInFlight = true
		b.probeStartedAt = b.now()
		return true, StateHalfOpen
	}

	return true, b.state
}

// RecordSuccess closes the breaker and clears the failure counters.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	was := b.state
	b.state = StateClosed
	b.consecutiveQuota = 0
	b.consecutiveFail = 0
	b.probeInFlight = false
	b.lastReason = ""
	b.mu.Unlock()

	if was != StateClosed {
		slog.Info("circuit breaker closed", "breaker", b.name)
		alert.Report(alert.Event{
			Level:     alert.LevelWarn,
			Component: b.name,
			Title:     "✅ recovered — circuit breaker closed",
		})
	}
}

// RecordQuotaFailure records a quota/billing failure. These trip the breaker
// fastest because they are deterministic: the next call will fail too.
func (b *Breaker) RecordQuotaFailure() { b.record(true, "quota/credits exhausted") }

// RecordFailure records a generic upstream failure.
func (b *Breaker) RecordFailure() { b.record(false, "repeated upstream failures") }

func (b *Breaker) record(quota bool, reason string) {
	b.mu.Lock()

	// A failed probe re-opens for longer than the initial trip.
	if b.state == StateHalfOpen {
		b.probeInFlight = false
		b.state = StateOpen
		b.openedAt = b.now()
		b.openUntil = b.openedAt.Add(b.reopenFor)
		b.lastReason = reason
		b.mu.Unlock()
		slog.Warn("circuit breaker probe failed, re-opening", "breaker", b.name, "for", b.reopenFor.String())
		return
	}

	if quota {
		b.consecutiveQuota++
	} else {
		b.consecutiveFail++
	}

	shouldTrip := b.state == StateClosed &&
		(b.consecutiveQuota >= b.quotaThreshold || b.consecutiveFail >= b.failureThreshold)
	if !shouldTrip {
		b.mu.Unlock()
		return
	}

	b.state = StateOpen
	b.openedAt = b.now()
	b.openUntil = b.openedAt.Add(b.openFor)
	b.lastReason = reason
	b.tripped++
	openFor := b.openFor
	b.mu.Unlock()

	slog.Error("circuit breaker opened", "breaker", b.name, "reason", reason, "for", openFor.String())
	alert.Report(alert.Event{
		Level:     alert.LevelFatal,
		Component: b.name,
		Title:     "circuit breaker OPEN — " + reason,
		Err:       fmt.Errorf("rejecting calls for %s", openFor),
		Fields:    map[string]string{"breaker": b.name, "reopen_after": openFor.String()},
	})
}

// State returns the current state, collapsing an expired open window to
// half-open so /health reports something actionable.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == StateOpen && !b.now().Before(b.openUntil) {
		return StateHalfOpen
	}
	return b.state
}

// Snapshot returns the breaker state for /health and /billing/status.
func (b *Breaker) Snapshot() map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := map[string]interface{}{
		"state":   string(b.state),
		"tripped": b.tripped,
	}
	if b.state != StateClosed {
		out["reason"] = b.lastReason
		out["retry_after_secs"] = int(b.openUntil.Sub(b.now()).Seconds())
		if v := out["retry_after_secs"].(int); v < 0 {
			out["retry_after_secs"] = 0
		}
	}
	return out
}

// Reset returns the breaker to its initial state. Test-only.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = StateClosed
	b.consecutiveQuota = 0
	b.consecutiveFail = 0
	b.probeInFlight = false
	b.tripped = 0
	b.lastReason = ""
}
