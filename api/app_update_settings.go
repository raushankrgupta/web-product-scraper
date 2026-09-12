package api

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// The admin-editable half of /app/config's app_update block.
//
// It is in Mongo rather than in an environment variable because it is flipped
// at the moment a Play release goes live, and that is not a moment anyone
// wants to be redeploying a backend. The admin panel writes the document; this
// process only ever reads it.

// appUpdateTTL is how long a read is reused. Short, because the whole point of
// the setting is that it changes at a moment of your choosing — and cheap,
// because /app/config is already cached for five minutes downstream, so this
// is one indexed read per minute per process at most.
const appUpdateTTL = 60 * time.Second

var (
	appUpdateMu     sync.RWMutex
	appUpdateCached models.AppUpdateSettings
	appUpdateAt     time.Time
	appUpdateLoaded bool
)

// loadAppUpdateSettings returns the current settings, cached.
//
// Every failure path resolves to the defaults, whose Mode is "off". That is
// the rule this function exists to enforce: a missing document, an
// unreachable database or a malformed record must never be able to produce a
// forced update, because a forced update that nobody can dismiss and no
// release can satisfy is an app that cannot be opened.
func loadAppUpdateSettings(ctx context.Context) models.AppUpdateSettings {
	appUpdateMu.RLock()
	if appUpdateLoaded && time.Since(appUpdateAt) < appUpdateTTL {
		cached := appUpdateCached
		appUpdateMu.RUnlock()
		return cached
	}
	appUpdateMu.RUnlock()

	settings := models.DefaultAppUpdateSettings()

	if utils.MongoReady() {
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		var stored models.AppUpdateSettings
		err := utils.GetCollection(config.DBName, models.CollAppSettings).
			FindOne(readCtx, bson.M{"_id": models.AppUpdateSettingsID}).
			Decode(&stored)
		if err == nil {
			settings = sanitizeAppUpdate(stored)
		}
		// A missing document is the normal state before the first save, not
		// an error worth logging on every cold start.
	}

	appUpdateMu.Lock()
	appUpdateCached, appUpdateAt, appUpdateLoaded = settings, time.Now(), true
	appUpdateMu.Unlock()

	return settings
}

// sanitizeAppUpdate coerces a stored document into something safe to serve.
//
// The admin form validates too, but this is the boundary that matters: the
// document is hand-editable in Mongo, and a mode of "FORCE " or a
// remind_after_hours of zero should degrade rather than reach a device.
func sanitizeAppUpdate(in models.AppUpdateSettings) models.AppUpdateSettings {
	d := models.DefaultAppUpdateSettings()
	out := in

	switch strings.ToLower(strings.TrimSpace(in.Mode)) {
	case models.UpdateModeSoft:
		out.Mode = models.UpdateModeSoft
	case models.UpdateModeForce:
		out.Mode = models.UpdateModeForce
	default:
		out.Mode = models.UpdateModeOff
	}

	out.LatestVersion = trimTo(in.LatestVersion, 20)
	out.MinSupportedVersion = trimTo(in.MinSupportedVersion, 20)

	// A prompt with nowhere to send the user is worse than no prompt, so an
	// unset store URL falls back to the canonical listing rather than
	// rendering a dead button.
	out.AndroidStoreURL = trimTo(in.AndroidStoreURL, 300)
	if out.AndroidStoreURL == "" {
		out.AndroidStoreURL = d.AndroidStoreURL
	}
	out.IOSStoreURL = trimTo(in.IOSStoreURL, 300)

	out.Title = fallback(trimTo(in.Title, 80), d.Title)
	out.Message = fallback(trimTo(in.Message, 300), d.Message)
	out.CTALabel = fallback(trimTo(in.CTALabel, 40), d.CTALabel)
	out.DismissLabel = fallback(trimTo(in.DismissLabel, 40), d.DismissLabel)

	highlights := make([]string, 0, 5)
	for _, h := range in.Highlights {
		if h = trimTo(h, 80); h != "" {
			highlights = append(highlights, h)
		}
		if len(highlights) == 5 {
			break
		}
	}
	out.Highlights = highlights

	out.RemindAfterHours = in.RemindAfterHours
	if out.RemindAfterHours <= 0 || out.RemindAfterHours > 24*30 {
		out.RemindAfterHours = d.RemindAfterHours
	}
	if out.Revision <= 0 {
		out.Revision = d.Revision
	}

	// The floor cannot be above the ceiling: a min_supported newer than the
	// version that is actually on the store would force every user to update
	// to something that does not exist yet.
	if out.Mode == models.UpdateModeForce && out.LatestVersion != "" &&
		utils.CompareVersions(out.MinSupportedVersion, out.LatestVersion) > 0 {
		out.MinSupportedVersion = out.LatestVersion
	}

	return out
}

// InvalidateAppUpdateCache drops the cached settings. Exported for tests.
func InvalidateAppUpdateCache() {
	appUpdateMu.Lock()
	appUpdateLoaded = false
	appUpdateMu.Unlock()
}

func trimTo(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
