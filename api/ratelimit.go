package api

import (
	"sync"
	"time"
)

// keyedLimiter is a small in-memory sliding-window rate limiter keyed by an
// arbitrary string (an IP, a user id). It is per-process: behind two replicas
// the effective limit doubles, which is acceptable for the abuse it exists to
// blunt (a script hammering an endpoint), and it needs no infrastructure.
//
// Started life as the guest-token limiter in guest_handler.go and was
// generalised when /product/upload needed the same thing: link import lets a
// user upload from any site with one tap, so an unbounded upload endpoint
// became an unbounded S3 bill.
type keyedLimiter struct {
	sync.Mutex
	history map[string][]time.Time
}

func newKeyedLimiter() *keyedLimiter {
	return &keyedLimiter{history: make(map[string][]time.Time)}
}

// allow records one event for key and reports whether it stays within limit
// events per window. A rejected call is not recorded, so a client that keeps
// retrying does not push its own recovery further away.
func (l *keyedLimiter) allow(key string, limit int, window time.Duration) bool {
	l.Lock()
	defer l.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)

	var valid []time.Time
	for _, t := range l.history[key] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= limit {
		l.history[key] = valid
		return false
	}

	valid = append(valid, now)
	l.history[key] = valid

	// Memory bound: prune keys whose newest event has expired once the map
	// grows past a few thousand entries.
	if len(l.history) > 5000 {
		for k, timestamps := range l.history {
			if len(timestamps) == 0 || timestamps[len(timestamps)-1].Before(cutoff) {
				delete(l.history, k)
			}
		}
	}

	return true
}
