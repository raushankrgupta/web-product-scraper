package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

var (
	MongoURI              string
	Port                  string
	GoogleClientID        string
	GoogleAndroidClientID string
	GoogleIOSClientID     string
	GoogleClientSecret    string
	GoogleRedirectURL     string
	GeminiAPIKey          string

	// --- Image generation providers ---
	//
	// OpenAI is a *fallback* for Gemini, not a product choice the user makes.
	// The app sells Standard and Pro; which vendor renders them is an
	// operational detail, which is why it lives here and in stars.json rather
	// than anywhere the client can see.
	OpenAIAPIKey string
	// OpenAIEnabled is the master switch. Off means the provider list is
	// exactly ["gemini"] and no OpenAI code path can be reached, whatever the
	// preference says.
	OpenAIEnabled bool
	// ImageProviderPreference is which vendor to try FIRST: "gemini" or
	// "openai". The other one becomes the fallback. Both tiers are priced so
	// that either vendor clears the margin floor (tools/stars_check enforces
	// it), so flipping this cannot make a generation unprofitable.
	ImageProviderPreference string
	// OpenAITimeoutSecs bounds a single OpenAI generation. Separate from the
	// Gemini budgets in stars.json because a fallback runs inside whatever is
	// left of the request, not with a fresh clock.
	OpenAITimeoutSecs int
	AWSRegion         string
	AWSBucketName     string
	DBName            string
	ContactEmail      string

	// ServerBScrapeURL is the full URL of the scraper-service (server B)
	// internal endpoint, e.g. https://scraper-b.example.com/internal/scrape.
	// When set, Myntra scrapes are delegated to server B (which runs on a
	// dynamic IP Myntra doesn't block) instead of being attempted from this
	// server's blocked datacenter IP. When empty, scraping happens locally
	// exactly as before (backwards compatible).
	ServerBScrapeURL string
	// InternalAPISecret is sent to server B in the X-Internal-Secret header so
	// B can verify the request came from this server. Must match B's value.
	InternalAPISecret string
	// ServerBEnabled is the master switch for the offload path. When false,
	// every scrape runs locally on this server, B is never contacted, and the
	// background health probe stops reporting it as a failed dependency.
	//
	// A configured URL is not the same as a usable one: in production this was
	// a Cloudflare *quick tunnel* hostname, which is regenerated on every
	// cloudflared restart and then stops resolving. The only way to stand the
	// offload path down was to blank SERVER_B_SCRAPE_URL out of the
	// environment, which also loses the URL. This flag turns the path off
	// while keeping the configuration on file.
	ServerBEnabled bool

	// --- Telegram failure notifier (utils/alert) ---
	//
	// AlertsEnabled defaults to false when the token or chat id is missing, so
	// a deployment that hasn't configured Telegram degrades to "no alerts"
	// rather than failing to boot.
	TelegramBotToken  string
	TelegramChatID    string
	AlertsEnabled     bool
	AlertMinLevel     string // warn | error | fatal
	AlertCooldownSecs int

	// Environment tags every alert and gates the dev-only test route.
	Environment string
	// AppVersion is injected at build time via -ldflags "-X main.version=...".
	// main.go copies it here at boot.
	AppVersion string

	// --- Timeouts ---
	//
	// The production log showed a single /try-on request hanging for 2m46s
	// against a 5-minute context. These budgets bound that.
	GeminiTimeoutSecs      int
	GeminiMultiTimeoutSecs int

	// S3UploadTimeoutSecs bounds a *persist* upload — one that stores a
	// result we have already paid to produce. It is deliberately independent
	// of the request deadline; see api.persistCtx.
	S3UploadTimeoutSecs int

	// --- Google Play billing (star purchases) ---
	//
	// Exactly one of these supplies the service-account credentials used to
	// verify purchase tokens. JSON is the deployment-friendly form: the whole
	// key sits in one env var, so nothing has to be mounted into the
	// container. When neither is set, purchase verification is disabled and
	// /billing/purchase refuses every request — refusing is the only safe
	// default, because crediting an unverified token is free stars for
	// anyone who can POST.
	PlayServiceAccountJSON string
	PlayServiceAccountFile string

	// PlayRTDNToken is a shared secret appended to the Pub/Sub push endpoint
	// as ?token=... . Google will happily push to any public URL, so without
	// it anyone who finds the endpoint can forge a refund notification.
	PlayRTDNToken string

	// StarsIdentityPepper salts the SHA-256 of an email address before it is
	// stored for returning-user detection. Without a pepper the hashes are a
	// plain dictionary of every address that ever signed up; with one, the
	// stored value is useless to anyone who does not also hold this secret.
	// Changing it resets everyone to "new user" — treat it as permanent.
	StarsIdentityPepper string

	// AllowedOrigins is the CORS allow-list. Empty means "*" (the previous
	// behaviour), which is kept as the default so an unconfigured deployment
	// doesn't suddenly break the mobile app.
	AllowedOrigins []string
)

// envInt reads an integer env var, falling back to def when unset or invalid.
func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("[config] %s=%q is not a positive integer, using %d", key, v, def)
		return def
	}
	return n
}

// envBool reads a boolean env var, falling back to def when unset or invalid.
func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("[config] %s=%q is not a boolean, using %v", key, v, def)
		return def
	}
	return b
}

// LoadConfig loads environment variables from .env file
func LoadConfig() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using default values or system environment variables")
	}

	MongoURI = os.Getenv("MONGO_URI")
	if MongoURI == "" {
		MongoURI = "mongodb://localhost:27017/"
	}

	Port = os.Getenv("PORT")
	if Port == "" {
		Port = "8080"
	}

	GoogleClientID = os.Getenv("GOOGLE_CLIENT_ID")
	GoogleAndroidClientID = os.Getenv("GOOGLE_ANDROID_CLIENT_ID")
	GoogleIOSClientID = os.Getenv("GOOGLE_IOS_CLIENT_ID")
	GoogleClientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
	GoogleRedirectURL = os.Getenv("GOOGLE_REDIRECT_URL")
	if GoogleRedirectURL == "" {
		GoogleRedirectURL = "http://localhost:8080/auth/google/callback"
	}

	GeminiAPIKey = os.Getenv("GEMINI_API_KEY")

	OpenAIAPIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	// Default off, and forced off without a key: an enabled provider we cannot
	// authenticate to would turn every fallback into a second wasted round
	// trip on the critical path.
	OpenAIEnabled = envBool("OPENAI_ENABLED", false) && OpenAIAPIKey != ""
	if !OpenAIEnabled && OpenAIAPIKey != "" && envBool("OPENAI_ENABLED", false) {
		log.Println("[config] OPENAI_ENABLED=true but OPENAI_API_KEY is empty — OpenAI disabled")
	}

	ImageProviderPreference = strings.ToLower(strings.TrimSpace(os.Getenv("IMAGE_PROVIDER_PREFERENCE")))
	switch ImageProviderPreference {
	case "", "gemini":
		ImageProviderPreference = "gemini"
	case "openai":
		if !OpenAIEnabled {
			// Preferring a disabled provider is a misconfiguration that would
			// otherwise be silent: every generation would quietly run on the
			// "fallback" and someone would wonder why the bill did not move.
			log.Println("[config] IMAGE_PROVIDER_PREFERENCE=openai but OpenAI is disabled — preferring gemini")
			ImageProviderPreference = "gemini"
		}
	default:
		log.Printf("[config] IMAGE_PROVIDER_PREFERENCE=%q is not a known provider, using gemini", ImageProviderPreference)
		ImageProviderPreference = "gemini"
	}

	OpenAITimeoutSecs = envInt("OPENAI_TIMEOUT_SECS", 90)

	AWSRegion = os.Getenv("AWS_REGION")
	if AWSRegion == "" {
		AWSRegion = "ap-south-1" // Default to Mumbai or user preference
	}
	AWSBucketName = os.Getenv("AWS_BUCKET_NAME")
	if AWSBucketName == "" {
		AWSBucketName = "tryonfusion"
	}

	DBName = os.Getenv("DB_NAME")
	if DBName == "" {
		DBName = "fitly"
	}

	ContactEmail = os.Getenv("CONTACT_EMAIL")
	if ContactEmail == "" {
		ContactEmail = "support@tryonfusion.com"
	}

	// Optional: delegate Myntra scrapes to server B. Left empty by default so
	// existing deployments keep scraping locally until B is configured.
	ServerBScrapeURL = strings.TrimSpace(os.Getenv("SERVER_B_SCRAPE_URL"))
	InternalAPISecret = os.Getenv("INTERNAL_API_SECRET")

	// Default preserves the previous behaviour exactly: on when a URL is
	// configured, off when it is not. An explicitly enabled B with no URL is
	// treated as off rather than as a boot error — there is nothing to call.
	ServerBEnabled = envBool("SERVER_B_ENABLED", ServerBScrapeURL != "") && ServerBScrapeURL != ""
	if !ServerBEnabled && ServerBScrapeURL != "" {
		log.Println("[config] SERVER_B_ENABLED=false — scraping locally, server B will not be contacted")
	}

	TelegramBotToken = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	TelegramChatID = strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	// Alerts are on by default *once credentials exist*, off otherwise.
	hasTelegram := TelegramBotToken != "" && TelegramChatID != ""
	AlertsEnabled = envBool("ALERTS_ENABLED", hasTelegram) && hasTelegram

	AlertMinLevel = strings.ToLower(strings.TrimSpace(os.Getenv("ALERT_MIN_LEVEL")))
	switch AlertMinLevel {
	case "warn", "error", "fatal", "info":
	default:
		AlertMinLevel = "warn"
	}

	AlertCooldownSecs = envInt("ALERT_COOLDOWN_SECS", 300)

	Environment = strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	if Environment == "" {
		Environment = "prod"
	}

	GeminiTimeoutSecs = envInt("GEMINI_TIMEOUT_SECS", 45)
	GeminiMultiTimeoutSecs = envInt("GEMINI_MULTI_TIMEOUT_SECS", 90)
	S3UploadTimeoutSecs = envInt("S3_UPLOAD_TIMEOUT_SECS", 30)

	PlayServiceAccountJSON = strings.TrimSpace(os.Getenv("PLAY_SERVICE_ACCOUNT_JSON"))
	PlayServiceAccountFile = strings.TrimSpace(os.Getenv("PLAY_SERVICE_ACCOUNT_FILE"))
	PlayRTDNToken = strings.TrimSpace(os.Getenv("PLAY_RTDN_TOKEN"))

	StarsIdentityPepper = strings.TrimSpace(os.Getenv("STARS_IDENTITY_PEPPER"))
	if StarsIdentityPepper == "" {
		// Falling back to the JWT secret keeps returning-user detection
		// working on a deployment that has not set a dedicated pepper, and
		// is still not a plain unsalted hash. Logged so it is visible.
		StarsIdentityPepper = os.Getenv("JWT_SECRET")
		if StarsIdentityPepper != "" {
			log.Println("[config] STARS_IDENTITY_PEPPER unset — falling back to JWT_SECRET")
		}
	}

	AllowedOrigins = nil
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			AllowedOrigins = append(AllowedOrigins, o)
		}
	}
}

// IsProd reports whether this process is running in the production
// environment. Used to gate dev-only routes.
func IsProd() bool { return Environment == "prod" }
