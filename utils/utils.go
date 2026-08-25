package utils

import (
	"context"
	"strings"
)

// AddToLogMessage appends a line to a handler-scoped log buffer.
//
// The buffer exists because handlers build up a narrative ("[Scrape API]",
// "Resolved URL: ...", "Scraping successful") and flushing each line
// separately would interleave unreadably under concurrency. It is retained as
// a shim so all 15 handlers keep working unchanged; what changed is the
// flush — see FlushLog.
func AddToLogMessage(logMessagesBuilder *strings.Builder, strToAdd string) {
	if logMessagesBuilder == nil {
		return
	}
	if logMessagesBuilder.Len() == logMessagesBuilder.Cap() {
		logMessagesBuilder.Grow(len(strToAdd))
	}

	logMessagesBuilder.WriteString(strToAdd)
	logMessagesBuilder.WriteString(";")
	logMessagesBuilder.WriteString("\n")
}

// FlushLog emits a handler's accumulated buffer as ONE structured record,
// replacing the deferred `fmt.Println(builder.String())` every handler used
// to run.
//
// That old pattern is directly responsible for two problems visible in the
// production log dump:
//
//  1. Multi-line blocks from concurrent requests interleaved, so a line could
//     not be attributed to a request — and `[Error] Quota exceeded`
//     consistently printed *before* the `[Virtual Try-On API]` header of its
//     own request, because the deferred flush ran after RespondError's direct
//     Println.
//  2. The output wasn't parseable. `docker compose logs | jq .` choked, so
//     none of it could be queried.
//
// Now each handler produces exactly one JSON object carrying the request id,
// so `jq 'select(.request_id=="…")'` reconstructs the whole story.
func FlushLog(ctx context.Context, b *strings.Builder) {
	if b == nil {
		return
	}
	steps := make([]string, 0, 8)
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), ";")
		if line != "" {
			steps = append(steps, line)
		}
	}
	if len(steps) == 0 {
		return
	}

	// The first line is the handler tag ("[Scrape API]") — promote it to the
	// message so records are groupable by handler.
	msg := steps[0]
	L(ctx).Info(msg, "steps", steps[1:])
}
