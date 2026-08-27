package config

import (
	"os"
	"testing"
)

func withEnv(t *testing.T, kv map[string]string, fn func()) {
	t.Helper()
	saved := map[string]*string{}
	for k, v := range kv {
		if old, ok := os.LookupEnv(k); ok {
			s := old
			saved[k] = &s
		} else {
			saved[k] = nil
		}
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
	defer func() {
		for k, v := range saved {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	}()
	fn()
}

// A missing token or chat id must degrade to "no alerts", never to a boot
// failure — that is the documented behaviour a deployment relies on.
func TestAlertsDisabledWithoutCredentials(t *testing.T) {
	cases := []struct {
		name              string
		token, chat, flag string
		want              bool
	}{
		{"no credentials at all", "", "", "", false},
		{"token only", "123:abc", "", "", false},
		{"chat only", "", "-100123", "", false},
		{"both set, flag unset", "123:abc", "-100123", "", true},
		{"both set, explicitly disabled", "123:abc", "-100123", "false", false},
		{"flag true but no credentials", "", "", "true", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withEnv(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": c.token,
				"TELEGRAM_CHAT_ID":   c.chat,
				"ALERTS_ENABLED":     c.flag,
			}, func() {
				LoadConfig()
				if AlertsEnabled != c.want {
					t.Errorf("AlertsEnabled = %v, want %v", AlertsEnabled, c.want)
				}
			})
		})
	}
}

func TestTimeoutDefaultsAndOverrides(t *testing.T) {
	withEnv(t, map[string]string{"GEMINI_TIMEOUT_SECS": "", "GEMINI_MULTI_TIMEOUT_SECS": ""}, func() {
		LoadConfig()
		if GeminiTimeoutSecs != 45 {
			t.Errorf("GeminiTimeoutSecs = %d, want the 45s default", GeminiTimeoutSecs)
		}
		if GeminiMultiTimeoutSecs != 90 {
			t.Errorf("GeminiMultiTimeoutSecs = %d, want the 90s default", GeminiMultiTimeoutSecs)
		}
	})

	withEnv(t, map[string]string{"GEMINI_TIMEOUT_SECS": "20"}, func() {
		LoadConfig()
		if GeminiTimeoutSecs != 20 {
			t.Errorf("GeminiTimeoutSecs = %d, want 20", GeminiTimeoutSecs)
		}
	})

	// A garbage value must fall back rather than produce a zero timeout,
	// which would cancel every request instantly.
	withEnv(t, map[string]string{"GEMINI_TIMEOUT_SECS": "banana"}, func() {
		LoadConfig()
		if GeminiTimeoutSecs != 45 {
			t.Errorf("GeminiTimeoutSecs = %d, want the default after an invalid value", GeminiTimeoutSecs)
		}
	})

	withEnv(t, map[string]string{"GEMINI_TIMEOUT_SECS": "0"}, func() {
		LoadConfig()
		if GeminiTimeoutSecs != 45 {
			t.Errorf("GeminiTimeoutSecs = %d, want the default; zero would cancel instantly", GeminiTimeoutSecs)
		}
	})
}

func TestAlertMinLevelFallsBackToWarn(t *testing.T) {
	withEnv(t, map[string]string{"ALERT_MIN_LEVEL": "nonsense"}, func() {
		LoadConfig()
		if AlertMinLevel != "warn" {
			t.Errorf("AlertMinLevel = %q, want warn", AlertMinLevel)
		}
	})
	withEnv(t, map[string]string{"ALERT_MIN_LEVEL": "FATAL"}, func() {
		LoadConfig()
		if AlertMinLevel != "fatal" {
			t.Errorf("AlertMinLevel = %q, want fatal", AlertMinLevel)
		}
	})
}

func TestEnvironmentDefaultsToProd(t *testing.T) {
	withEnv(t, map[string]string{"ENVIRONMENT": ""}, func() {
		LoadConfig()
		if Environment != "prod" || !IsProd() {
			t.Errorf("Environment = %q, IsProd() = %v; want prod/true", Environment, IsProd())
		}
	})
	withEnv(t, map[string]string{"ENVIRONMENT": "dev"}, func() {
		LoadConfig()
		if IsProd() {
			t.Error("IsProd() should be false in dev — it gates the alert-test route")
		}
	})
}

func TestAllowedOriginsParsing(t *testing.T) {
	withEnv(t, map[string]string{"ALLOWED_ORIGINS": " https://a.com , https://b.com ,"}, func() {
		LoadConfig()
		if len(AllowedOrigins) != 2 || AllowedOrigins[0] != "https://a.com" || AllowedOrigins[1] != "https://b.com" {
			t.Errorf("AllowedOrigins = %v", AllowedOrigins)
		}
	})
	withEnv(t, map[string]string{"ALLOWED_ORIGINS": ""}, func() {
		LoadConfig()
		if len(AllowedOrigins) != 0 {
			t.Errorf("AllowedOrigins = %v, want empty (which means '*')", AllowedOrigins)
		}
	})
}

// The offload path must default to exactly the previous behaviour — on when a
// URL is configured, off when it isn't — so adding the flag cannot change how
// an existing deployment scrapes until someone sets it deliberately.
func TestServerBEnabledDefaults(t *testing.T) {
	const url = "https://b.example.com/internal/scrape"

	cases := []struct {
		name    string
		url     string
		enabled string
		want    bool
	}{
		{"url set, flag unset", url, "", true},
		{"url unset, flag unset", "", "", false},
		{"url set, explicitly off", url, "false", false},
		{"url set, explicitly on", url, "true", true},
		// Nothing to call: an enabled B with no URL is off, not a boot error.
		{"no url, explicitly on", "", "true", false},
		// A typo must not silently stand the scraper down.
		{"url set, garbage flag", url, "yes-please", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, map[string]string{
				"SERVER_B_SCRAPE_URL": tc.url,
				"SERVER_B_ENABLED":    tc.enabled,
			}, func() {
				LoadConfig()
				if ServerBEnabled != tc.want {
					t.Errorf("ServerBEnabled = %v, want %v", ServerBEnabled, tc.want)
				}
				// The URL is kept on file either way — that is the point of
				// having a separate switch.
				if ServerBScrapeURL != tc.url {
					t.Errorf("ServerBScrapeURL = %q, want %q", ServerBScrapeURL, tc.url)
				}
			})
		})
	}
}
