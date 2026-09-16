package api

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// GetAppHandler serves GET /get — the one link the app shares.
//
// Share captions and referral links used to be the Play Store URL, which is
// wrong in both directions once there is an iOS app: an iPhone user sharing it
// sends their friends to Google Play, and App Review rejects an iOS app that
// names another platform (guideline 2.3.10). A link to our own site avoids
// both — it sends each visitor to the store for their device.
//
//	Android → the Play listing, with the referral code in `referrer`, which
//	          Play hands back to the installed app.
//	iPhone  → the App Store listing. The App Store has no install referrer, so
//	          when the link carries a code the visitor first sees it, with a
//	          button onwards, rather than losing it in the redirect.
//	Other   → a page with both store links and the code.
func GetAppHandler(w http.ResponseWriter, r *http.Request) {
	code := utils.NormaliseReferralCode(r.URL.Query().Get("ref"))
	if len(code) < 4 {
		code = ""
	}
	settings := loadAppUpdateSettings(r.Context())
	playURL := playStoreURL(settings, code)
	iosURL := appStoreURL(r.Context(), settings)
	ua := r.UserAgent()

	w.Header().Set("Cache-Control", "no-store")

	switch {
	case isAndroidUA(ua):
		http.Redirect(w, r, playURL, http.StatusFound)
		return
	case isAppleMobileUA(ua) && iosURL != "" && code == "":
		http.Redirect(w, r, iosURL, http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = getAppTmpl.Execute(w, getAppPageData{
		Code:     code,
		PlayURL:  playURL,
		AppStore: iosURL,
		IsIPhone: isAppleMobileUA(ua),
	})
}

func playStoreURL(settings models.AppUpdateSettings, code string) string {
	base := settings.AndroidStoreURL
	if base == "" {
		base = models.DefaultAppUpdateSettings().AndroidStoreURL
	}
	if code == "" {
		return base
	}
	sep := "&"
	if !strings.Contains(base, "?") {
		sep = "?"
	}
	return base + sep + "referrer=" + url.QueryEscape(code)
}

// appStoreURL prefers the admin-set listing URL, then one built from the
// numeric App Store id. Empty until the iOS app exists.
func appStoreURL(_ context.Context, settings models.AppUpdateSettings) string {
	if settings.IOSStoreURL != "" {
		return settings.IOSStoreURL
	}
	if config.AppleAppID != "" {
		return "https://apps.apple.com/app/id" + url.PathEscape(config.AppleAppID)
	}
	return ""
}

func isAndroidUA(ua string) bool {
	return strings.Contains(strings.ToLower(ua), "android")
}

func isAppleMobileUA(ua string) bool {
	l := strings.ToLower(ua)
	return strings.Contains(l, "iphone") || strings.Contains(l, "ipad") || strings.Contains(l, "ipod")
}

type getAppPageData struct {
	Code     string
	PlayURL  string
	AppStore string
	IsIPhone bool
}

var getAppTmpl = template.Must(template.New("get").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Get TryOnFusion</title>
<meta name="robots" content="noindex">
<style>
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:#1A0A2E;color:#fff;
       min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;box-sizing:border-box}
  .card{max-width:420px;width:100%;text-align:center}
  img.logo{width:88px;height:88px;border-radius:20px}
  h1{font-size:26px;margin:16px 0 8px}
  p{color:#d9d0ea;line-height:1.5;margin:0 0 20px}
  .code{background:#fff;color:#1A0A2E;border-radius:14px;padding:16px;margin:0 0 20px}
  .code small{display:block;color:#6b5b86;margin-bottom:6px}
  .code strong{font-size:30px;letter-spacing:4px}
  a.btn{display:block;background:#fff;color:#1A0A2E;text-decoration:none;font-weight:700;padding:15px;border-radius:12px;margin:10px 0}
  a.btn.secondary{background:transparent;color:#fff;border:1px solid #6b5b86}
</style>
</head>
<body>
<div class="card">
  <img class="logo" src="/logo.png" alt="TryOnFusion">
  <h1>TryOnFusion</h1>
  <p>Try on any outfit before you buy it.</p>
  {{if .Code}}
  <div class="code"><small>Your friend's code — enter it when you sign up</small><strong>{{.Code}}</strong></div>
  {{end}}
  {{if .IsIPhone}}
    {{if .AppStore}}<a class="btn" href="{{.AppStore}}">Download on the App Store</a>
    {{else}}<p>The iPhone app is coming soon.</p>{{end}}
  {{else}}
    {{if .AppStore}}<a class="btn" href="{{.AppStore}}">Download on the App Store</a>{{end}}
    <a class="btn{{if .AppStore}} secondary{{end}}" href="{{.PlayURL}}">Get it on Google Play</a>
  {{end}}
</div>
</body>
</html>`))
