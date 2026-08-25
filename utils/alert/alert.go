// Package alert delivers operational failure notifications to Telegram.
//
// Design constraints (see FIX_PLAN.md §1.1) — every one of these is a hard
// requirement, because an alerting system that can break the thing it is
// watching is worse than no alerting at all:
//
//   - Never block a request. Report() is a non-blocking enqueue onto a
//     buffered channel; when the buffer is full we drop and count.
//   - Never crash the app. The worker recovers from its own panics and
//     restarts itself; transport errors are logged, never propagated.
//   - Never flood. Fingerprint dedup + per-fingerprint cooldown + a rollup
//     ticker (see dedup.go).
//   - Never leak secrets. All rendered text goes through redact() (format.go).
//   - Togglable without a redeploy: ALERTS_ENABLED, read at boot.
package alert

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level is the severity of an event. Ordering matters: see levelRank.
type Level string

const (
	LevelInfo  Level = "INFO"
	LevelWarn  Level = "WARN"
	LevelError Level = "ERROR"
	LevelFatal Level = "FATAL"
)

func levelRank(l Level) int {
	switch Level(strings.ToUpper(string(l))) {
	case LevelInfo:
		return 0
	case LevelWarn:
		return 1
	case LevelError:
		return 2
	case LevelFatal:
		return 3
	default:
		return 1
	}
}

// Component names the subsystem an event came from. Kept as a plain string
// (not an enum) so a new call site never needs a change here, but stick to
// the known set so fingerprints stay stable:
// gemini | scraper | serverb | auth | s3 | mongo | http | tryon | system.
type Event struct {
	Level     Level
	Component string
	Title     string // short and stable — it is part of the fingerprint
	Err       error
	RequestID string
	UserID    string
	Route     string
	Method    string
	Status    int
	Latency   time.Duration
	Fields    map[string]string
	Stack     string // panics only

	// Force delivers the event regardless of AlertMinLevel. Reserved for
	// low-volume lifecycle events (boot, shutdown) that an operator needs to
	// see even when the channel is turned down to errors-only — the
	// post-deploy check is literally "did the 🚀 boot message arrive?".
	Force bool

	// rollup is set internally by the dedup flusher; it is never populated
	// by callers.
	rollup int
	at     time.Time
}

// sender is the transport. Swapped in tests.
type sender interface {
	Send(text string) error
}

var (
	mu       sync.RWMutex
	enabled  bool
	minLevel = LevelWarn
	env      = "prod"
	version  = "dev"
	tx       sender

	ch      chan Event
	dropped atomic.Int64
	sent    atomic.Int64

	started  bool
	stopOnce sync.Once
	done     chan struct{}
	wg       sync.WaitGroup
)

// Config is the subset of the app config this package needs. Passing it in
// (rather than importing config) keeps the dependency one-directional and
// makes the package testable without env vars.
type Config struct {
	BotToken     string
	ChatID       string
	Enabled      bool
	MinLevel     string
	CooldownSecs int
	Environment  string
	AppVersion   string
}

// Init starts the background worker. It is a no-op when alerting is disabled
// (missing token/chat id, or ALERTS_ENABLED=false), which is the intended
// degradation for a deployment that hasn't configured Telegram yet.
func Init(cfg Config) {
	mu.Lock()
	defer mu.Unlock()

	if started {
		return
	}

	env = orDefault(cfg.Environment, "prod")
	version = orDefault(cfg.AppVersion, "dev")
	if cfg.MinLevel != "" {
		minLevel = Level(strings.ToUpper(cfg.MinLevel))
	}
	setCooldown(time.Duration(cfg.CooldownSecs) * time.Second)

	if !cfg.Enabled || cfg.BotToken == "" || cfg.ChatID == "" {
		enabled = false
		log.Printf("[alert] disabled (enabled=%v token_set=%v chat_set=%v)",
			cfg.Enabled, cfg.BotToken != "", cfg.ChatID != "")
		return
	}

	tx = newTelegram(cfg.BotToken, cfg.ChatID)
	enabled = true
	started = true
	ch = make(chan Event, 256)
	done = make(chan struct{})

	wg.Add(2)
	go func() { defer wg.Done(); worker() }()
	go func() { defer wg.Done(); rollupLoop(done) }()

	log.Printf("[alert] enabled env=%s version=%s min_level=%s cooldown=%s", env, version, minLevel, cooldown())
}

// InitForTest wires the package to an in-memory transport. Test-only.
func InitForTest(s sender, min Level, cd time.Duration) {
	mu.Lock()
	tx = s
	enabled = true
	started = true
	minLevel = min
	env, version = "test", "test"
	ch = make(chan Event, 256)
	done = make(chan struct{})
	mu.Unlock()

	setCooldown(cd)
	resetDedup()

	wg.Add(1)
	go func() { defer wg.Done(); worker() }()
}

// Shutdown drains the queue so alerts buffered at SIGTERM still get sent.
// Safe to call when alerting was never enabled.
func Shutdown(ctx context.Context) {
	mu.RLock()
	on := started
	mu.RUnlock()
	if !on {
		return
	}

	stopOnce.Do(func() {
		close(done)
		close(ch)
	})

	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()

	select {
	case <-drained:
	case <-ctx.Done():
		log.Printf("[alert] shutdown timed out with %d queued", len(ch))
	}

	if d := dropped.Load(); d > 0 {
		log.Printf("[alert] %d events were dropped (queue full) over this process's lifetime", d)
	}
}

// Enabled reports whether alerts are actually being delivered.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return enabled
}

// Stats exposes counters for /health.
func Stats() (sentN, droppedN int64, queued int) {
	mu.RLock()
	c := ch
	mu.RUnlock()
	if c != nil {
		queued = len(c)
	}
	return sent.Load(), dropped.Load(), queued
}

// Report enqueues an event. It never blocks and never panics — a full queue
// increments the drop counter instead of applying backpressure to the request
// that is trying to report a failure.
func Report(e Event) {
	mu.RLock()
	on, min, c := enabled, minLevel, ch
	mu.RUnlock()

	if !on || c == nil {
		return
	}
	if !e.Force && levelRank(e.Level) < levelRank(min) {
		return
	}
	if e.at.IsZero() {
		e.at = time.Now()
	}

	defer func() {
		// A send on a closed channel (Shutdown raced with an in-flight
		// request) must not take down the request goroutine.
		if r := recover(); r != nil {
			dropped.Add(1)
		}
	}()

	select {
	case c <- e:
	default:
		dropped.Add(1)
	}
}

// kv turns a flat ...string list into a field map. An odd trailing element is
// ignored rather than panicking — an alert call site must never be the thing
// that crashes a handler.
func kv(pairs []string) map[string]string {
	if len(pairs) == 0 {
		return nil
	}
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i] == "" {
			continue
		}
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

func Warnf(component, title string, err error, pairs ...string) {
	Report(Event{Level: LevelWarn, Component: component, Title: title, Err: err, Fields: kv(pairs)})
}

func Errorf(component, title string, err error, pairs ...string) {
	Report(Event{Level: LevelError, Component: component, Title: title, Err: err, Fields: kv(pairs)})
}

func Fatalf(component, title string, err error, pairs ...string) {
	Report(Event{Level: LevelFatal, Component: component, Title: title, Err: err, Fields: kv(pairs)})
}

func Infof(component, title string, pairs ...string) {
	Report(Event{Level: LevelInfo, Component: component, Title: title, Fields: kv(pairs)})
}

// Lifecycle reports a boot/shutdown event. Always delivered, so an operator
// can tell "the deploy landed" from "the deploy is stuck" without lowering
// ALERT_MIN_LEVEL.
func Lifecycle(title string, pairs ...string) {
	Report(Event{Level: LevelInfo, Component: "system", Title: title, Fields: kv(pairs), Force: true})
}

func worker() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[alert] worker panic: %v — restarting", r)
			wg.Add(1)
			go func() { defer wg.Done(); worker() }()
		}
	}()

	for e := range ch {
		if suppress(e) {
			continue
		}
		deliver(e)
	}
}

func deliver(e Event) {
	mu.RLock()
	t := tx
	mu.RUnlock()
	if t == nil {
		return
	}
	if err := t.Send(format(e)); err != nil {
		log.Printf("[alert] send failed: %v", err)
		return
	}
	sent.Add(1)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
