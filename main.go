package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/raushankrgupta/web-product-scraper/api"
	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// version is injected at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)"
//
// It tags every Telegram alert and is reported by /health, so an alert can be
// traced to the exact build that produced it.
var version = "dev"

func main() {
	// Install the JSON logger before anything can log — config.LoadConfig
	// itself prints ("No .env file found…"), and a single non-JSON line is
	// enough to break `docker compose logs app | jq .`. It is re-installed
	// below once the environment name is known.
	utils.InitLogger(os.Getenv("LOG_LEVEL"), "unknown", version)

	config.LoadConfig()
	config.AppVersion = version
	api.AppVersion = version

	utils.InitLogger(os.Getenv("LOG_LEVEL"), config.Environment, version)

	// The star economy is loaded before anything can serve a request, and a
	// bad config is fatal rather than a warning. Every other setting in this
	// process degrades to a default when it is wrong; this one must not. A
	// typo that prices a Pro generation at 1 star instead of 25 is a silent,
	// uncapped bill, and a missing pack definition means a user pays and
	// receives nothing.
	if err := config.LoadStars(); err != nil {
		alert.Fatalf("config", "star configuration is invalid", err)
		flushAlerts()
		slog.Error("invalid star configuration", "error", err)
		os.Exit(1)
	}

	// Alerting second, so a failure in either of the two boot dependencies
	// below reaches Telegram instead of only the container log.
	alert.Init(alert.Config{
		BotToken:     config.TelegramBotToken,
		ChatID:       config.TelegramChatID,
		Enabled:      config.AlertsEnabled,
		MinLevel:     config.AlertMinLevel,
		CooldownSecs: config.AlertCooldownSecs,
		Environment:  config.Environment,
		AppVersion:   version,
	})

	if err := utils.ConnectMongo(config.MongoURI); err != nil {
		alert.Fatalf("mongo", "failed to connect at boot", err)
		flushAlerts()
		slog.Error("failed to connect to MongoDB", "error", err)
		os.Exit(1)
	}

	if err := utils.InitS3(); err != nil {
		alert.Fatalf("s3", "failed to initialise at boot", err)
		flushAlerts()
		slog.Error("failed to initialize S3", "error", err)
		os.Exit(1)
	}

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	// Indexes are idempotent and non-fatal; see EnsureIndexes.
	idxCtx, cancelIdx := context.WithTimeout(rootCtx, 30*time.Second)
	utils.EnsureIndexes(idxCtx, config.DBName)
	cancelIdx()

	// Return stars from generations that never reported back — a crashed
	// process, a dropped connection, a handler that panicked between
	// reserving and settling. Without this those stars are lost to the user
	// with no image to show for them.
	utils.StartHoldSweeper(rootCtx)

	// Credit Indian UPI and netbanking purchases that settle after the app
	// has moved on, retry consumption so Play does not auto-refund a
	// purchase we already credited, and claw back refunds and chargebacks.
	utils.StartPurchaseReconciler(rootCtx)

	// Probe server B once now and every 5 minutes. A dead scrape dependency
	// should be visible at boot, not on the first user request.
	api.StartServerBHealthChecks(rootCtx)

	mux := http.NewServeMux()
	registerRoutes(mux)

	// Outermost first: recover wraps everything so a panic anywhere still
	// produces a 500 and an alert; the request id is established before the
	// request log so both the log line and any alert carry it.
	handler := api.RecoverMiddleware(
		api.RequestIDMiddleware(
			api.RequestLogMiddleware(
				corsMiddleware(mux))))

	srv := &http.Server{
		Addr:    ":" + config.Port,
		Handler: handler,
		// A slow-loris client must not be able to hold a connection open
		// indefinitely. WriteTimeout is generous because a try-on response
		// can legitimately take the full generation budget.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		slog.Info("server starting", "port", config.Port, "env", config.Environment, "version", version)
		alert.Lifecycle("🚀 service booted", "port", config.Port, "version", version, "env", config.Environment)

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			alert.Fatalf("system", "listener died", err)
			flushAlerts()
			slog.Error("server failed to start", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: let in-flight try-ons finish and flush the alert
	// queue, so a deploy doesn't drop a generation the user is waiting on.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	slog.Info("shutdown signal received, draining")
	alert.Lifecycle("graceful shutdown started", "version", version)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	cancelRoot()
	utils.CloseGemini()
	flushAlerts()
	slog.Info("shutdown complete")
}

// flushAlerts drains the alert queue with a bounded wait. Called on every
// exit path so a fatal boot error still reaches Telegram.
func flushAlerts() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alert.Shutdown(ctx)
}

// corsMiddleware applies the CORS headers. The allow-list comes from
// ALLOWED_ORIGINS; when it is unset the behaviour is the previous `*`, so an
// unconfigured deployment doesn't break the mobile app on upgrade.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		switch {
		case len(config.AllowedOrigins) == 0:
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case origin != "" && originAllowed(origin):
			w.Header().Set("Access-Control-Allow-Origin", origin)
			// Vary matters once the value depends on the request: without it
			// a shared cache can serve one origin's response to another.
			w.Header().Add("Vary", "Origin")
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept, X-Request-ID, Cache-Control, Pragma")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(origin string) bool {
	for _, a := range config.AllowedOrigins {
		if strings.EqualFold(a, origin) || a == "*" {
			return true
		}
	}
	return false
}

// post/get/etc. are tiny helpers that make the route table below readable:
// every route now declares which verbs it accepts, and MethodGuard rejects
// the rest with a logged 405 rather than a silent 401 from AuthMiddleware.
func registerRoutes(mux *http.ServeMux) {
	guard := api.MethodGuard

	// Static site.
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	// Health — unauthenticated, cheap, and the thing to point an uptime
	// monitor at.
	mux.Handle("/health", guard([]string{http.MethodGet, http.MethodHead}, http.HandlerFunc(api.HealthHandler)))

	// Public auth routes.
	mux.Handle("/auth/signup", guard(post, http.HandlerFunc(api.SignupHandler)))
	mux.Handle("/auth/verify-otp", guard(post, http.HandlerFunc(api.VerifyOTPHandler)))
	mux.Handle("/auth/login", guard(post, http.HandlerFunc(api.LoginHandler)))
	mux.Handle("/auth/google", guard(post, http.HandlerFunc(api.GoogleLoginHandler)))
	mux.Handle("/auth/guest", guard(post, http.HandlerFunc(api.GuestTokenHandler)))
	mux.Handle("/auth/forgot-password", guard(post, http.HandlerFunc(api.ForgotPasswordHandler)))
	mux.Handle("/auth/reset-password", guard(post, http.HandlerFunc(api.ResetPasswordHandler)))
	mux.Handle("/auth/change-password", guard(post, api.AuthMiddleware(http.HandlerFunc(api.ChangePasswordHandler))))
	// One URL, two audiences. GET/HEAD serve the human-facing deletion page
	// that Google Play's Data Safety review crawls — it was getting a 405 on
	// every crawl, which fails a review quietly and weeks later. DELETE is the
	// canonical API verb; POST is accepted because DELETE-with-body is awkward
	// in several HTTP clients. Auth is applied inside DeleteAccountRoute, on
	// the API branch only: the instructions page has to be readable by a
	// reviewer, and by a user who can no longer sign in.
	mux.Handle("/auth/delete-account", guard(
		[]string{http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPost},
		api.DeleteAccountRoute()))

	// Billing: balance, catalogue, purchases and history.
	mux.Handle("/billing/status", guard(get, api.AuthMiddleware(http.HandlerFunc(api.BillingStatusHandler))))
	// The catalogue is what the app renders prices from, so a repricing in
	// config/stars.json reaches users without a store release.
	mux.Handle("/billing/catalog", guard(get, api.AuthMiddleware(http.HandlerFunc(api.CatalogHandler))))
	mux.Handle("/billing/purchase", guard(post, api.AuthMiddleware(http.HandlerFunc(api.PurchaseHandler))))
	mux.Handle("/billing/ledger", guard(get, api.AuthMiddleware(http.HandlerFunc(api.LedgerHandler))))

	// Google Play Real-time Developer Notifications, delivered by a Pub/Sub
	// push subscription. Google is the caller, so this sits outside
	// AuthMiddleware and is guarded by the shared token in PLAY_RTDN_TOKEN
	// instead — an open endpoint that mutates balances would let anyone who
	// finds the URL forge a refund.
	mux.Handle("/billing/play-rtdn", guard(post, http.HandlerFunc(api.PlayRTDNHandler)))

	// Legal.
	mux.Handle("/legal/privacy-policy", guard(get, http.HandlerFunc(api.GetPrivacyPolicy)))
	mux.Handle("/legal/terms-of-service", guard(get, http.HandlerFunc(api.GetTermsOfService)))

	// Product.
	mux.Handle("/product/details", guard(post, api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.ScrapeHandler)), true)))
	mux.Handle("/product/upload", guard(post, api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.UploadProductHandler)), true)))

	mux.Handle("/themes", guard(get, api.ImageCacheMiddleware(http.HandlerFunc(api.GetThemesHandler), true)))

	// Person endpoints return user-specific JSON (not raw images), so we don't
	// wrap them in ImageCacheMiddleware. That middleware set
	// `Cache-Control: public, max-age=86400`, which (a) caused the client to
	// serve a stale list for 24h after a delete, masking the soft-delete in
	// the DB, and (b) allowed shared caches to store user-specific data.
	// The handler uses ETag-based revalidation via RespondJSONWithETag instead.
	persons := api.AuthMiddleware(http.HandlerFunc(api.PersonHandler))
	mux.Handle("/persons", guard([]string{http.MethodGet, http.MethodPost}, persons))
	mux.Handle("/persons/", guard([]string{http.MethodGet, http.MethodPut, http.MethodDelete}, persons))

	// Try-on endpoints:
	//   MethodGuard → AuthMiddleware → TryOnGuardMiddleware → StarGateMiddleware
	//
	// TryOnGuardMiddleware sits *outside* the billing check on purpose: a
	// duplicate in-flight request or a user stuck in a failure loop should be
	// rejected before it can take a second star hold, and before anything
	// reaches a paid upstream call. The production log showed 30 requests
	// covering 11 unique pairs, all uncapped.
	//
	// StarGateMiddleware replaces the old QuotaMiddleware: it reserves the
	// cost of the generation before the handler runs and settles it after —
	// committed on success, refunded on every failure path.
	tryOn := func(h http.HandlerFunc) http.Handler {
		return guard(post, api.AuthMiddleware(api.TryOnGuardMiddleware(api.StarGateMiddleware(h))))
	}
	mux.Handle("/try-on", tryOn(api.VirtualTryOnHandler))
	mux.Handle("/try-on/individual", tryOn(api.IndividualTryOnHandler))
	mux.Handle("/try-on/couple", tryOn(api.CoupleTryOnHandler))
	mux.Handle("/try-on/group", tryOn(api.GroupTryOnHandler))
	// Guest try-on: one-shot endpoint for anonymous users (no persistence).
	// Same sandwich because guest tokens come through the same path with
	// plan=guest. Guests are pinned to the free quality tier and capped by
	// free.guest_daily_free_count.
	mux.Handle("/try-on/guest", tryOn(api.GuestTryOnHandler))

	// Gallery, wardrobe, and feedback all return user-specific JSON (not raw
	// images), and the response also contains presigned S3 URLs that expire
	// quickly — so they use ETag revalidation, not ImageCacheMiddleware.
	gallery := api.AuthMiddleware(http.HandlerFunc(api.GalleryHandler))
	mux.Handle("/gallery", guard([]string{http.MethodGet, http.MethodPost}, gallery))
	mux.Handle("/gallery/", guard([]string{http.MethodGet, http.MethodPost, http.MethodDelete}, gallery))

	mux.Handle("/feedback", guard(post, api.AuthMiddleware(http.HandlerFunc(api.FeedbackHandler))))

	wardrobe := api.AuthMiddleware(http.HandlerFunc(api.WardrobeHandler))
	mux.Handle("/wardrobe", guard([]string{http.MethodGet, http.MethodPost}, wardrobe))
	mux.Handle("/wardrobe/", guard([]string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}, wardrobe))

	// Dev-only routes. Both are gated on ENVIRONMENT != prod *and* the
	// internal shared secret, so they can never be reached in production.
	//
	// /internal/stars is the one that matters for testing: in-app purchases
	// cannot complete on a sideloaded build, so without a way to grant a
	// balance directly, every paid path is untestable until an AAB reaches a
	// Play track.
	if !config.IsProd() {
		mux.Handle("/internal/alert-test", guard(post, http.HandlerFunc(api.AlertTestHandler)))
		mux.Handle("/internal/stars", guard(post, http.HandlerFunc(api.DevStarsHandler)))
	}
}

var (
	get  = []string{http.MethodGet}
	post = []string{http.MethodPost}
)
