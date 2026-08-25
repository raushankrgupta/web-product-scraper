package alert

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Telegram hard-caps a sendMessage payload at 4096 characters.
const telegramMaxLen = 4096

func emoji(l Level) string {
	switch Level(strings.ToUpper(string(l))) {
	case LevelFatal:
		return "🔴"
	case LevelError:
		return "🟠"
	case LevelWarn:
		return "🟡"
	default:
		return "🔵"
	}
}

var (
	// Redaction patterns. These run over the *rendered* message, so they
	// also cover anything a caller stuffed into Fields without thinking.
	reAPIKeyQuery = regexp.MustCompile(`(?i)([?&](?:key|api_?key|access_?token|token|signature|x-amz-signature|x-amz-credential)=)[^&\s"']+`)
	reBearer      = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]+`)
	reAuthHeader  = regexp.MustCompile(`(?i)(authorization["':\s]+)[^\s"',]+`)
	reTGToken     = regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_\-]{30,}\b`)
	// Presigned S3 URLs carry the signature in the query string; drop the
	// whole query rather than trying to enumerate every AWS param.
	rePresigned = regexp.MustCompile(`(https?://[^\s"']*?)\?[^\s"']*[Xx]-[Aa]mz-[^\s"']*`)
)

// redact strips credentials from anything we are about to send to a third
// party. Telegram messages are stored on Telegram's servers indefinitely, so
// a leaked presigned URL or API key there is a real disclosure.
func redact(s string) string {
	s = rePresigned.ReplaceAllString(s, "$1?<redacted>")
	s = reAPIKeyQuery.ReplaceAllString(s, "${1}<redacted>")
	s = reBearer.ReplaceAllString(s, "${1}<redacted>")
	s = reAuthHeader.ReplaceAllString(s, "${1}<redacted>")
	s = reTGToken.ReplaceAllString(s, "<redacted>")
	return s
}

// esc escapes the three characters Telegram's HTML parse mode cares about.
// (This is the whole reason we use HTML and not MarkdownV2, which needs 18.)
func esc(s string) string { return html.EscapeString(s) }

// shortID renders an identifier as head…tail so a Telegram message stays
// scannable without losing the ability to grep the full value in the logs.
func shortID(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:8] + "…" + s[len(s)-4:]
}

func format(e Event) string {
	if e.at.IsZero() {
		e.at = time.Now()
	}

	mu.RLock()
	envName, ver := env, version
	mu.RUnlock()

	var b strings.Builder

	if e.rollup > 0 {
		fmt.Fprintf(&b, "🔁 <b>%s: %s</b> — %d more in the last %s\n",
			esc(e.Component), esc(e.Title), e.rollup, cooldown().Round(time.Second))
		fmt.Fprintf(&b, "<i>%s · %s</i>", esc(envName), e.at.Format("2006-01-02 15:04:05 MST"))
		return truncate(redact(b.String()))
	}

	fmt.Fprintf(&b, "%s <b>%s</b> · %s · %s\n\n",
		emoji(e.Level), strings.ToUpper(string(e.Level)), esc(envName), esc(ver))
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(e.Title))

	row := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&b, "%-10s %s\n", k+":", esc(v))
	}
	row("Component", e.Component)
	if e.Method != "" || e.Route != "" {
		row("Route", strings.TrimSpace(e.Method+" "+e.Route))
	}
	if e.Status != 0 {
		row("Status", fmt.Sprintf("%d", e.Status))
	}
	if e.Latency > 0 {
		row("Latency", e.Latency.Round(time.Millisecond).String())
	}
	row("User", shortID(e.UserID))
	row("Request", shortID(e.RequestID))

	if len(e.Fields) > 0 {
		b.WriteString("\n")
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if e.Fields[k] == "" {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", esc(k), esc(e.Fields[k]))
		}
	}

	if e.Err != nil {
		fmt.Fprintf(&b, "\n<b>error:</b> <code>%s</code>\n", esc(e.Err.Error()))
	}
	if e.Stack != "" {
		fmt.Fprintf(&b, "\n<pre>%s</pre>\n", esc(e.Stack))
	}

	fmt.Fprintf(&b, "\n<i>%s</i>", e.at.Format("2006-01-02 15:04:05 MST"))

	return truncate(redact(b.String()))
}

// truncate cuts an over-long message so the final string — closing tags and
// suffix included — still fits Telegram's 4096-character cap. It cuts on a
// rune boundary (invalid UTF-8 is rejected outright) and never leaves an
// unbalanced tag behind (unbalanced HTML is a 400 "can't parse entities",
// which loses the whole alert).
func truncate(s string) string {
	const suffix = "\n… (truncated)"
	if len(s) <= telegramMaxLen {
		return s
	}

	// Reserve room for the suffix plus the longest set of closers we might
	// need to append.
	budget := telegramMaxLen - len(suffix) - len(closerBudget)
	if budget < 0 {
		budget = 0
	}

	out := s[:budget]
	// Back off to a rune boundary.
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	// If the cut landed inside a tag, drop the partial tag.
	if i := strings.LastIndex(out, "<"); i > strings.LastIndex(out, ">") {
		out = out[:i]
	}
	// Re-close any block tag the cut left open.
	for _, tag := range []string{"pre", "code", "b", "i"} {
		if strings.Count(out, "<"+tag+">") > strings.Count(out, "</"+tag+">") {
			out += "</" + tag + ">"
		}
	}
	return out + suffix
}

// closerBudget is the worst case set of closing tags truncate may need to
// append, reserved up front so the result can never exceed the cap.
const closerBudget = "</pre></code></b></i>"
