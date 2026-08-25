package alert

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// memSender captures what would have gone to Telegram.
type memSender struct {
	mu   sync.Mutex
	msgs []string
}

func (m *memSender) Send(text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, text)
	return nil
}

func (m *memSender) all() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.msgs))
	copy(out, m.msgs)
	return out
}

// waitFor polls until cond holds or the deadline passes. The worker is
// asynchronous, so a bare assertion would be flaky.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// The production log carried 38 byte-identical quota messages in six days.
// Five identical events inside the cooldown must produce exactly one send,
// plus one rollup line when flushed.
func TestDedupSuppressesIdenticalEvents(t *testing.T) {
	s := &memSender{}
	InitForTest(s, LevelWarn, time.Minute)

	for i := 0; i < 5; i++ {
		Errorf("gemini", "quota exhausted", errors.New("Error 429: prepayment credits are depleted"))
	}

	if !waitFor(t, time.Second, func() bool { return len(s.all()) >= 1 }) {
		t.Fatal("no alert was delivered")
	}
	// Give any (incorrect) extra sends a chance to land.
	time.Sleep(100 * time.Millisecond)

	if got := len(s.all()); got != 1 {
		t.Fatalf("delivered %d messages for 5 identical events, want 1:\n%s",
			got, strings.Join(s.all(), "\n---\n"))
	}

	flushRollups()
	msgs := s.all()
	if len(msgs) != 2 {
		t.Fatalf("after rollup flush got %d messages, want 2", len(msgs))
	}
	if !strings.Contains(msgs[1], "4 more") {
		t.Errorf("rollup should report 4 suppressed events, got: %s", msgs[1])
	}
	if !strings.HasPrefix(msgs[1], "🔁") {
		t.Errorf("rollup should use the rollup emoji, got: %s", msgs[1])
	}
}

// Two quota errors that differ only in ids/urls/numbers are the same class of
// failure and must share a fingerprint.
func TestErrorClassNormalisesVariableParts(t *testing.T) {
	a := errorClass(errors.New("quota exceeded for person 6a8aa6e60fd107d8f1f216a4 after 3 retries, see https://aistudio.google.com/billing?key=abc"))
	b := errorClass(errors.New("quota exceeded for person 6a86d61b0fd107d8f1f21683 after 17 retries, see https://aistudio.google.com/billing?key=xyz"))
	if a != b {
		t.Errorf("errorClass should collapse ids/urls/numbers:\n a=%q\n b=%q", a, b)
	}

	if errorClass(nil) != "" {
		t.Error("errorClass(nil) should be empty")
	}

	// Genuinely different failures must NOT collapse.
	c := errorClass(errors.New("context deadline exceeded"))
	if a == c {
		t.Error("unrelated errors collapsed onto the same class")
	}
}

func TestFingerprintDistinguishesComponentAndLevel(t *testing.T) {
	base := Event{Level: LevelError, Component: "gemini", Title: "blocked", Err: errors.New("x")}

	other := base
	other.Component = "scraper"
	if fingerprint(base) == fingerprint(other) {
		t.Error("different components must fingerprint differently")
	}

	other = base
	other.Level = LevelFatal
	if fingerprint(base) == fingerprint(other) {
		t.Error("different levels must fingerprint differently")
	}

	same := base
	same.RequestID = "abc" // not part of the fingerprint
	if fingerprint(base) != fingerprint(same) {
		t.Error("request id must not affect the fingerprint")
	}
}

func TestMinLevelFiltersLowerSeverity(t *testing.T) {
	s := &memSender{}
	InitForTest(s, LevelError, time.Minute)

	Warnf("gemini", "this should be filtered", errors.New("warn"))
	Errorf("gemini", "this should pass", errors.New("err"))

	if !waitFor(t, time.Second, func() bool { return len(s.all()) >= 1 }) {
		t.Fatal("the ERROR event was never delivered")
	}
	time.Sleep(100 * time.Millisecond)

	msgs := s.all()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (the WARN must be filtered)", len(msgs))
	}
	if !strings.Contains(msgs[0], "should pass") {
		t.Errorf("wrong message delivered: %s", msgs[0])
	}
}

func TestReportNeverBlocksWhenQueueIsFull(t *testing.T) {
	// A sender that blocks forever simulates Telegram being unreachable.
	block := make(chan struct{})
	InitForTest(blockingSender{block}, LevelWarn, time.Minute)

	done := make(chan struct{})
	go func() {
		// Far more than the 256-slot buffer.
		for i := 0; i < 2000; i++ {
			Errorf("gemini", fmt.Sprintf("event %d", i), errors.New("x"))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Report blocked the caller — it must drop instead of applying backpressure")
	}

	_, dropped, _ := Stats()
	if dropped == 0 {
		t.Error("expected some events to be dropped once the queue filled")
	}
	close(block)
}

type blockingSender struct{ block chan struct{} }

func (b blockingSender) Send(string) error {
	<-b.block
	return nil
}

func TestFormatRedactsSecrets(t *testing.T) {
	e := Event{
		Level:     LevelError,
		Component: "gemini",
		Title:     "generation failed",
		Err: errors.New("GET https://generativelanguage.googleapis.com/v1beta/models?key=AIzaSyTOPSECRET123 failed; " +
			"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig"),
		Fields: map[string]string{
			"person_url": "https://tryonfusion.s3.ap-south-1.amazonaws.com/person/1.jpg?X-Amz-Signature=deadbeefcafe&X-Amz-Credential=AKIAEXAMPLE",
		},
	}

	out := format(e)

	for _, secret := range []string{"AIzaSyTOPSECRET123", "eyJhbGciOiJIUzI1NiJ9", "deadbeefcafe", "AKIAEXAMPLE"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked into the alert body:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "redacted") {
		t.Errorf("expected a redaction marker in:\n%s", out)
	}
}

func TestFormatEscapesHTML(t *testing.T) {
	out := format(Event{
		Level: LevelError, Component: "http", Title: "bad <script>alert(1)</script> input",
		Err: errors.New("a & b < c"),
	})
	if strings.Contains(out, "<script>") {
		t.Errorf("unescaped HTML in message:\n%s", out)
	}
	if !strings.Contains(out, "&amp;") {
		t.Errorf("ampersand was not escaped:\n%s", out)
	}
}

func TestTruncateStaysUnderTelegramLimit(t *testing.T) {
	huge := strings.Repeat("x", 20000)
	out := format(Event{
		Level: LevelFatal, Component: "http", Title: "panic",
		Err: errors.New(huge), Stack: huge,
	})
	if len(out) > telegramMaxLen {
		t.Fatalf("formatted message is %d bytes, over the %d limit", len(out), telegramMaxLen)
	}
	if !strings.Contains(out, "truncated") {
		t.Error("truncated message should say so")
	}
	// Balanced tags, or Telegram rejects the whole message with a 400.
	if strings.Count(out, "<pre>") != strings.Count(out, "</pre>") {
		t.Errorf("unbalanced <pre> after truncation:\n%s", out[len(out)-200:])
	}
}

func TestDisabledAlertsAreANoOp(t *testing.T) {
	mu.Lock()
	enabled = false
	mu.Unlock()
	defer func() {
		mu.Lock()
		enabled = true
		mu.Unlock()
	}()

	// Must not panic and must not block.
	Errorf("gemini", "ignored", errors.New("x"))
}
