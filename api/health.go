package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// Server B is the scraper that runs on a residential/dynamic IP, because
// Myntra blocks this server's datacenter address. In the production log its
// hostname was a Cloudflare *quick tunnel*
// (dig-bytes-radar-witness.trycloudflare.com) — a name that is randomly
// regenerated every time cloudflared restarts and simply stops resolving
// afterwards. Both the Myntra path and guest try-on depended on it and both
// were down, and nobody knew until a log dump six days later.
//
// A dead dependency should be known at boot, not on the first user request —
// and one we have deliberately switched off (SERVER_B_ENABLED=false) should
// not be probed, alerted on, or counted against /health at all.

var (
	serverBMu      sync.RWMutex
	serverBHealthy bool
	serverBReason  string
	serverBChecked time.Time

	bootTime = time.Now()
)

// AppVersion is set from main at boot so /health can report it.
var AppVersion = "dev"

// checkServerB probes B's endpoint. A configured-but-unreachable B is an
// error; a B that is unconfigured *or* switched off via SERVER_B_ENABLED is
// simply "not in use" and healthy by definition (scraping stays local, which
// is the documented fallback). Only an offload path we are actually relying on
// can be broken.
func checkServerB(ctx context.Context) (bool, string) {
	if !config.ServerBEnabled {
		if config.ServerBScrapeURL == "" {
			return true, "not configured (scraping locally)"
		}
		return true, "disabled (scraping locally)"
	}

	// An empty-URL probe: B rejects it as a bad request, which still proves
	// DNS, TLS, routing and the shared secret all work.
	resp, err := callServerB(ctx, "healthcheck", "", false)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return false, fmt.Sprintf("server B returned %d", resp.StatusCode)
	}
	return true, fmt.Sprintf("reachable (%d)", resp.StatusCode)
}

// ServerBHealthy reports the last known state without doing any I/O.
func ServerBHealthy() (bool, string) {
	serverBMu.RLock()
	defer serverBMu.RUnlock()
	if serverBChecked.IsZero() {
		return true, "not yet checked"
	}
	return serverBHealthy, serverBReason
}

// StartServerBHealthChecks probes B once at boot and then every 5 minutes,
// alerting only on a healthy↔unhealthy *transition* so a long outage is one
// message rather than one per poll.
func StartServerBHealthChecks(ctx context.Context) {
	// A switched-off B is not a dependency. Record the state once and start
	// no ticker: polling a host we have deliberately stood down only produces
	// noise, and (when the URL is a dead quick tunnel) a DNS lookup every five
	// minutes for as long as the process runs.
	if !config.ServerBEnabled {
		ok, reason := checkServerB(ctx)

		serverBMu.Lock()
		serverBHealthy, serverBReason, serverBChecked = ok, reason, time.Now()
		serverBMu.Unlock()

		slog.Info("server B offload path inactive", "reason", reason)
		return
	}

	run := func() {
		probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		ok, reason := checkServerB(probeCtx)
		cancel()

		serverBMu.Lock()
		changed := serverBChecked.IsZero() || ok != serverBHealthy
		serverBHealthy, serverBReason, serverBChecked = ok, reason, time.Now()
		serverBMu.Unlock()

		if !changed {
			return
		}
		if ok {
			slog.Info("server B healthy", "reason", reason)
			alert.Report(alert.Event{
				Level: alert.LevelWarn, Component: "serverb",
				Title:  "✅ server B reachable again",
				Fields: map[string]string{"detail": reason},
			})
			return
		}
		slog.Error("server B unreachable", "reason", reason, "url", config.ServerBScrapeURL)
		alert.Errorf("serverb", "scraper service unreachable", fmt.Errorf("%s", reason),
			"url", redactHost(config.ServerBScrapeURL))
	}

	run() // boot check, synchronous — we want this in the boot logs

	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}

// redactHost keeps the hostname (which is the useful part of a server B
// failure) and drops the path.
func redactHost(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.Index(rest, "/"); j > 0 {
			return raw[:i+3] + rest[:j]
		}
	}
	return raw
}

// HealthHandler reports dependency status for an external uptime monitor.
// It is intentionally unauthenticated and cheap: no Gemini call, and Mongo /
// S3 checks are bounded at 2 seconds each.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		utils.RespondJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	mongoOK := "ok"
	if utils.Client == nil {
		mongoOK = "not initialised"
	} else {
		pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
		if err := utils.Client.Ping(pingCtx, nil); err != nil {
			mongoOK = "fail: " + err.Error()
		}
		pingCancel()
	}

	bOK, bReason := ServerBHealthy()
	sentN, droppedN, queued := alert.Stats()

	body := map[string]interface{}{
		"status":  "ok",
		"version": AppVersion,
		"env":     config.Environment,
		"uptime":  time.Since(bootTime).Round(time.Second).String(),
		"mongo":   mongoOK,
		"s3":      s3Status(),
		"server_b": map[string]interface{}{
			"enabled": config.ServerBEnabled,
			"healthy": bOK,
			"detail":  bReason,
		},
		"gemini": utils.GeminiBreaker.Snapshot(),
		"alerts": map[string]interface{}{
			"enabled": alert.Enabled(),
			"sent":    sentN,
			"dropped": droppedN,
			"queued":  queued,
		},
	}

	status := http.StatusOK
	if mongoOK != "ok" || !bOK {
		body["status"] = "degraded"
		// Still 200: a degraded dependency shouldn't make an uptime monitor
		// page for something the app can partially serve. 503 is reserved
		// for "cannot serve at all".
	}
	if utils.Client == nil {
		status = http.StatusServiceUnavailable
		body["status"] = "unhealthy"
	}

	utils.RespondJSON(w, status, body)
}

func s3Status() string {
	if config.AWSBucketName == "" {
		return "not configured"
	}
	return "ok"
}
