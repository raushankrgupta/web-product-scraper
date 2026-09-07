package api

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// AppConfigResponse is what GET /app/config returns.
//
// It is the remote switchboard for the Link Import feature: which fetch path
// a client should use, the limits it should apply, and which hosts it must
// refuse to open. Nothing in it is secret, and the guest flow needs it before
// any login, so the endpoint is unauthenticated.
//
// Additive only. Clients embed defaults for every field and must tolerate
// unknown fields, so a new key never breaks an old client and a missing key
// never breaks a new one.
type AppConfigResponse struct {
	Schema        int       `json:"schema"`
	GeneratedAt   time.Time `json:"generated_at"`
	MinAppVersion string    `json:"min_app_version"`

	LinkImport   LinkImportConfig   `json:"link_import"`
	ServerScrape ServerScrapeConfig `json:"server_scrape"`
}

// LinkImportConfig is what a >= 2.4.0 client needs to run the in-app browser
// import. See fitly-app/docs/USER_SIDE_LINK_IMPORT_PLAN.md §7.3.
type LinkImportConfig struct {
	// Mode: "device" — fetch on the phone; "server" — legacy /product/details.
	Mode          string   `json:"mode"`
	MaxImages     int      `json:"max_images"`
	MaxCandidates int      `json:"max_candidates"`
	MinImagePx    int      `json:"min_image_px"`
	UploadMaxEdge int      `json:"upload_max_edge"`
	JPEGQuality   float64  `json:"jpeg_quality"`
	BlockedHosts  []string `json:"blocked_hosts"`
	NoticeVersion int      `json:"notice_version"`
}

// ServerScrapeConfig describes the state of the legacy server-side path.
type ServerScrapeConfig struct {
	Mode   string `json:"mode"`
	Sunset string `json:"sunset,omitempty"`
}

// appConfigMaxAge is how long clients and shared caches may hold the config.
// Five minutes bounds how stale a rollback flip (LINK_IMPORT_MODE=server) or a
// blocked-host addition can be on a device that is already running.
const appConfigMaxAge = 300 * time.Second

// appConfigGeneratedAt is when this process loaded its config. The response is
// a pure function of the environment, so this is the honest "generated" time
// and — unlike time.Now() per request — keeps the ETag stable between calls.
var appConfigGeneratedAt = time.Now()

// AppConfigHandler serves GET /app/config.
//
// Not routed through utils.RespondJSONWithETag on purpose: that helper sets
// `Cache-Control: private, no-cache`, which is right for per-user payloads and
// wrong for this one. The config is identical for everyone and is fetched on
// every cold start, so it should be publicly cacheable for a short window.
func AppConfigHandler(w http.ResponseWriter, r *http.Request) {
	data, err := json.Marshal(buildAppConfig(appConfigGeneratedAt))
	if err != nil {
		utils.RespondError(w, nil, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	etag := fmt.Sprintf(`"%x"`, md5.Sum(data))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(appConfigMaxAge.Seconds())))
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
	w.Write([]byte("\n"))
}

// buildAppConfig assembles the response from config. Split out so tests can
// pin the shape without an HTTP round trip.
func buildAppConfig(now time.Time) AppConfigResponse {
	blocked := config.LinkImportBlockedHosts
	if blocked == nil {
		// An explicit empty array, not null: the client iterates it.
		blocked = []string{}
	}
	return AppConfigResponse{
		Schema:        1,
		GeneratedAt:   now.UTC().Truncate(time.Second),
		MinAppVersion: config.MinAppVersion,
		LinkImport: LinkImportConfig{
			Mode:          config.LinkImportMode,
			MaxImages:     config.LinkImportMaxImages,
			MaxCandidates: config.LinkImportMaxCandidates,
			MinImagePx:    200,
			UploadMaxEdge: 1024,
			JPEGQuality:   0.7,
			BlockedHosts:  blocked,
			NoticeVersion: config.LinkImportNoticeVersion,
		},
		ServerScrape: ServerScrapeConfig{
			Mode:   config.ServerScrapeMode,
			Sunset: config.ServerScrapeSunset,
		},
	}
}
