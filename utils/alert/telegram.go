package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// telegram is the outbound transport. It owns its own http.Client with an
// explicit timeout — using http.DefaultClient here would mean a hung Telegram
// connection pins the alert worker forever and every subsequent alert is
// dropped for queue-full.
type telegram struct {
	token   string
	chatID  string
	apiBase string
	client  *http.Client
	sleep   func(time.Duration) // injectable for tests
}

// defaultAPIBase is Telegram's real endpoint. TELEGRAM_API_BASE overrides it
// so a staging box (or a local smoke test) can point at a stub and verify the
// whole alert pipeline without touching a real bot.
const defaultAPIBase = "https://api.telegram.org"

func newTelegram(token, chatID string) *telegram {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("TELEGRAM_API_BASE")), "/")
	if base == "" {
		base = defaultAPIBase
	}
	return &telegram{
		token:   token,
		chatID:  chatID,
		apiBase: base,
		client:  &http.Client{Timeout: 10 * time.Second},
		sleep:   time.Sleep,
	}
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Send posts one message, retrying on transport errors and 5xx with a
// 1s → 3s → 9s backoff. A 429 is honoured once using Telegram's own
// retry_after. Any other 4xx is logged and dropped: the message is malformed
// or the chat is gone, and retrying it forever would starve the queue.
func (t *telegram) Send(text string) error {
	base := t.apiBase
	if base == "" {
		base = defaultAPIBase
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", base, t.token)

	payload, err := json.Marshal(map[string]interface{}{
		"chat_id":                  t.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}

	backoff := time.Second
	var lastErr error
	rateLimited := false

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			t.sleep(backoff)
			backoff *= 3
		}

		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := t.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			return nil

		case resp.StatusCode == http.StatusTooManyRequests:
			if rateLimited {
				return fmt.Errorf("telegram rate limited twice, dropping")
			}
			rateLimited = true
			var tr telegramResponse
			wait := 5 * time.Second
			if json.Unmarshal(body, &tr) == nil && tr.Parameters.RetryAfter > 0 {
				wait = time.Duration(tr.Parameters.RetryAfter) * time.Second
			}
			if wait > 60*time.Second {
				return fmt.Errorf("telegram asked for a %s wait, dropping", wait)
			}
			log.Printf("[alert] telegram 429, waiting %s", wait)
			t.sleep(wait)
			// Don't consume a retry slot for the rate-limit pause.
			attempt--

		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("telegram %d: %s", resp.StatusCode, string(body))

		default: // 4xx other than 429 — permanent, don't retry
			return fmt.Errorf("telegram %d (not retrying): %s", resp.StatusCode, string(body))
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("telegram send failed after retries")
	}
	return lastErr
}
