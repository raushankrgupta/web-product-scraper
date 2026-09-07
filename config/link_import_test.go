package config

import "testing"

func TestLinkImportConfig_Defaults(t *testing.T) {
	withEnv(t, map[string]string{
		"LINK_IMPORT_MODE": "", "SERVER_SCRAPE_MODE": "", "LINK_IMPORT_BLOCKED_HOSTS": "",
		"LINK_IMPORT_MAX_IMAGES": "", "MIN_APP_VERSION": "",
	}, func() {
		loadLinkImportConfig()
		if LinkImportMode != "device" {
			t.Errorf("LinkImportMode = %q", LinkImportMode)
		}
		if ServerScrapeMode != "deprecated" {
			t.Errorf("ServerScrapeMode = %q", ServerScrapeMode)
		}
		if LinkImportMaxImages != 6 || MinAppVersion != "2.3.4" {
			t.Errorf("defaults: max=%d min=%q", LinkImportMaxImages, MinAppVersion)
		}
	})
}

func TestLinkImportConfig_UnknownEnumFallsBack(t *testing.T) {
	withEnv(t, map[string]string{"LINK_IMPORT_MODE": "cloud", "SERVER_SCRAPE_MODE": "maybe"}, func() {
		loadLinkImportConfig()
		if LinkImportMode != "device" || ServerScrapeMode != "deprecated" {
			t.Errorf("got %q / %q", LinkImportMode, ServerScrapeMode)
		}
	})
}

func TestLinkImportConfig_MaxImagesCapped(t *testing.T) {
	withEnv(t, map[string]string{"LINK_IMPORT_MAX_IMAGES": "50", "LINK_IMPORT_MODE": "", "SERVER_SCRAPE_MODE": ""}, func() {
		loadLinkImportConfig()
		if LinkImportMaxImages != 8 {
			t.Errorf("max images = %d, want 8", LinkImportMaxImages)
		}
	})
}

func TestHostBlockedForLinkImport(t *testing.T) {
	withEnv(t, map[string]string{"LINK_IMPORT_BLOCKED_HOSTS": " Www.Blocked.com, other.example ", "LINK_IMPORT_MODE": "", "SERVER_SCRAPE_MODE": ""}, func() {
		loadLinkImportConfig()
		for host, want := range map[string]bool{
			"blocked.com": true, "www.blocked.com": true, "cdn.blocked.com": true,
			"notblocked.com": false, "blocked.com.evil": false, "other.example": true, "": false,
		} {
			if got := HostBlockedForLinkImport(host); got != want {
				t.Errorf("%q: got %v want %v", host, got, want)
			}
		}
	})
}
