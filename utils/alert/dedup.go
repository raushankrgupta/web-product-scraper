package alert

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The log this package was built from contained 38 byte-identical "prepayment
// credits are depleted" messages inside six days. Delivering those as 38
// Telegram messages is how an alert channel gets muted, so every event is
// fingerprinted and suppressed for a cooldown window; suppressed events are
// then summarised by rollupLoop as a single "🔁 … N more" line.

type entry struct {
	lastSent   time.Time
	suppressed int
	sample     Event
}

var (
	dedupMu sync.Mutex
	seen    = map[string]*entry{}

	cooldownMu sync.RWMutex
	cooldownD  = 5 * time.Minute
)

func setCooldown(d time.Duration) {
	if d <= 0 {
		d = 5 * time.Minute
	}
	cooldownMu.Lock()
	cooldownD = d
	cooldownMu.Unlock()
}

func cooldown() time.Duration {
	cooldownMu.RLock()
	defer cooldownMu.RUnlock()
	return cooldownD
}

func resetDedup() {
	dedupMu.Lock()
	seen = map[string]*entry{}
	dedupMu.Unlock()
}

var (
	reObjectID = regexp.MustCompile(`\b[0-9a-fA-F]{24}\b`)
	reUUID     = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reURL      = regexp.MustCompile(`https?://[^\s"']+`)
	reNumber   = regexp.MustCompile(`\d+`)
	reSpace    = regexp.MustCompile(`\s+`)
)

// errorClass normalises an error message down to its shape, so that two
// failures that differ only in ids, urls, byte counts or durations collapse
// onto the same fingerprint. Without this, "quota exceeded for project 123"
// and "quota exceeded for project 456" would be two separate alert streams.
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	s = reURL.ReplaceAllString(s, "<url>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reObjectID.ReplaceAllString(s, "<id>")
	s = reNumber.ReplaceAllString(s, "<n>")
	s = reSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func fingerprint(e Event) string {
	sum := sha1.Sum([]byte(strings.Join([]string{
		string(e.Level), e.Component, e.Title, errorClass(e.Err),
	}, "|")))
	return hex.EncodeToString(sum[:8])
}

// suppress reports whether this event should be swallowed because an
// identical one was delivered inside the cooldown window. Suppressed events
// are counted and surfaced later by rollupLoop.
func suppress(e Event) bool {
	// Rollup lines bypass their own dedup — they *are* the dedup output.
	if e.rollup > 0 {
		return false
	}

	fp := fingerprint(e)
	now := time.Now()

	dedupMu.Lock()
	defer dedupMu.Unlock()

	ent, ok := seen[fp]
	if !ok {
		seen[fp] = &entry{lastSent: now, sample: e}
		return false
	}
	if now.Sub(ent.lastSent) >= cooldown() {
		ent.lastSent = now
		ent.suppressed = 0
		ent.sample = e
		return false
	}
	ent.suppressed++
	ent.sample = e
	return true
}

// rollupLoop emits one summary line per fingerprint that accumulated
// suppressed events, then clears the counter. Entries that have been quiet
// for 10× the cooldown are reaped so the map can't grow without bound.
func rollupLoop(stop <-chan struct{}) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			flushRollups()
			return
		case <-t.C:
			flushRollups()
		}
	}
}

func flushRollups() {
	now := time.Now()
	var out []Event

	dedupMu.Lock()
	for fp, ent := range seen {
		if ent.suppressed > 0 {
			e := ent.sample
			e.rollup = ent.suppressed
			e.at = now
			out = append(out, e)
			ent.suppressed = 0
			ent.lastSent = now
			continue
		}
		if now.Sub(ent.lastSent) > 10*cooldown() {
			delete(seen, fp)
		}
	}
	dedupMu.Unlock()

	for _, e := range out {
		deliver(e)
	}
}
