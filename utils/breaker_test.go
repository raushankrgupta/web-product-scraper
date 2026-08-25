package utils

import (
	"testing"
	"time"
)

// newTestBreaker builds a breaker with a controllable clock so the state
// machine can be exercised without sleeping.
func newTestBreaker() (*Breaker, func(time.Duration)) {
	b := NewBreaker("test")
	now := time.Now()
	b.now = func() time.Time { return now }
	return b, func(d time.Duration) { now = now.Add(d) }
}

func TestBreakerOpensAfterTwoQuotaFailures(t *testing.T) {
	b, _ := newTestBreaker()

	if ok, _ := b.Allow(); !ok {
		t.Fatal("a fresh breaker must allow calls")
	}

	b.RecordQuotaFailure()
	if got := b.State(); got != StateClosed {
		t.Fatalf("after 1 quota failure state = %s, want closed", got)
	}
	if ok, _ := b.Allow(); !ok {
		t.Fatal("breaker must still allow after a single quota failure")
	}

	b.RecordQuotaFailure()
	if got := b.State(); got != StateOpen {
		t.Fatalf("after 2 quota failures state = %s, want open", got)
	}

	// This is the whole point: the third request costs nothing.
	if ok, st := b.Allow(); ok {
		t.Fatalf("open breaker allowed a call (state %s)", st)
	}
}

func TestBreakerHalfOpenProbeThenClose(t *testing.T) {
	b, advance := newTestBreaker()

	b.RecordQuotaFailure()
	b.RecordQuotaFailure()

	// Still inside the open window.
	advance(4 * time.Minute)
	if ok, _ := b.Allow(); ok {
		t.Fatal("breaker allowed a call before the open window elapsed")
	}

	// Window elapsed: exactly one probe gets through.
	advance(2 * time.Minute)
	ok, st := b.Allow()
	if !ok || st != StateHalfOpen {
		t.Fatalf("expected a half-open probe, got ok=%v state=%s", ok, st)
	}
	if ok, _ := b.Allow(); ok {
		t.Fatal("a second caller was admitted while a probe was in flight")
	}

	b.RecordSuccess()
	if got := b.State(); got != StateClosed {
		t.Fatalf("after a successful probe state = %s, want closed", got)
	}
	if ok, _ := b.Allow(); !ok {
		t.Fatal("closed breaker rejected a call")
	}
}

func TestBreakerFailedProbeReopensForLonger(t *testing.T) {
	b, advance := newTestBreaker()

	b.RecordQuotaFailure()
	b.RecordQuotaFailure()
	advance(6 * time.Minute)

	if ok, _ := b.Allow(); !ok {
		t.Fatal("expected a probe to be admitted")
	}
	b.RecordQuotaFailure() // probe failed

	if got := b.State(); got != StateOpen {
		t.Fatalf("after a failed probe state = %s, want open", got)
	}
	// The re-open window is 10 minutes, longer than the initial 5.
	advance(6 * time.Minute)
	if ok, _ := b.Allow(); ok {
		t.Fatal("breaker re-opened for less than the reopen window")
	}
	advance(5 * time.Minute)
	if ok, _ := b.Allow(); !ok {
		t.Fatal("breaker never recovered to half-open after the reopen window")
	}
}

// A probe whose caller never reports back (it bailed before reaching the
// upstream) must not wedge the breaker shut forever.
func TestBreakerAbandonedProbeDoesNotWedge(t *testing.T) {
	b, advance := newTestBreaker()

	b.RecordQuotaFailure()
	b.RecordQuotaFailure()
	advance(6 * time.Minute)

	if ok, _ := b.Allow(); !ok {
		t.Fatal("expected a probe")
	}
	// Caller vanishes. After probeTimeout another caller may try.
	advance(probeTimeout + time.Second)
	if ok, _ := b.Allow(); !ok {
		t.Fatal("breaker stayed wedged after an abandoned probe")
	}
}

func TestBreakerGenericFailuresNeedMoreToTrip(t *testing.T) {
	b, _ := newTestBreaker()

	for i := 0; i < 4; i++ {
		b.RecordFailure()
	}
	if got := b.State(); got != StateClosed {
		t.Fatalf("after 4 generic failures state = %s, want closed", got)
	}
	b.RecordFailure()
	if got := b.State(); got != StateOpen {
		t.Fatalf("after 5 generic failures state = %s, want open", got)
	}
}

func TestBreakerSuccessResetsCounters(t *testing.T) {
	b, _ := newTestBreaker()

	b.RecordQuotaFailure()
	b.RecordSuccess()
	b.RecordQuotaFailure()

	if got := b.State(); got != StateClosed {
		t.Fatalf("state = %s, want closed — a success must reset the consecutive counter", got)
	}
}

func TestIsQuotaError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		// The exact message from the production log.
		{errStr("googleapi: Error 429: You exceeded your current quota. Your prepayment credits are depleted"), true},
		{errStr("rpc error: code = ResourceExhausted"), true},
		{errStr("context deadline exceeded"), false},
		{errStr("no content generated (blocked, finish_reason=IMAGE_SAFETY)"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsQuotaError(c.err); got != c.want {
			t.Errorf("IsQuotaError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
