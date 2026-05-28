package myntra_scraper

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/tebeka/selenium"
	"github.com/tebeka/selenium/chrome"
)

const chromeDriverPath = "/usr/bin/chromedriver"

// seleniumBasePort / seleniumPortRange are deliberately disjoint from the
// 4444-4459 range used by scrapers/base. With concurrent requests, two
// scrapers spinning up ChromeDriver on the same TCP port would race; this
// gives the Myntra path its own 4500-4515 window.
const (
	seleniumBasePort  = 4500
	seleniumPortRange = 16
)

// FetchDocumentSelenium fetches the URL via a full ChromeDriver browser
// instance (Strategy 3 / last resort). It picks a free port from this
// package's port manager, spawns ChromeDriver on it, and tears it down
// when the request completes.
func (b *baseScraper) FetchDocumentSelenium(url string) (*goquery.Document, error) {
	initPortManager(seleniumBasePort, seleniumPortRange)

	port, err := globalPortManager.GetPort()
	if err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}
	defer globalPortManager.ReleasePort(port)

	opts := []selenium.ServiceOption{}

	driverPath := os.Getenv("CHROMEDRIVER_PATH")
	if driverPath == "" {
		driverPath = chromeDriverPath
	}

	service, err := selenium.NewChromeDriverService(driverPath, port, opts...)
	if err != nil {
		return nil, fmt.Errorf("error starting Chrome driver service: %v", err)
	}
	defer service.Stop()

	caps := selenium.Capabilities{"browserName": "chrome"}

	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	chromeArgs := []string{
		"--headless=new",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-blink-features=AutomationControlled",
		"--disable-extensions",
		"--disable-gpu",
		"--window-size=1920,1080",
		fmt.Sprintf("--user-agent=%s", userAgent),
	}

	// Mirror the HTTP client's proxy so Selenium fallbacks also exit
	// through the configured residential proxy. See scraper.go for the
	// SCRAPER_PROXY_URL contract.
	if proxy := ScraperProxyRaw(); proxy != "" {
		chromeArgs = append(chromeArgs, fmt.Sprintf("--proxy-server=%s", proxy))
		if strings.EqualFold(os.Getenv("SCRAPER_PROXY_INSECURE_TLS"), "1") ||
			strings.EqualFold(os.Getenv("SCRAPER_PROXY_INSECURE_TLS"), "true") {
			chromeArgs = append(chromeArgs, "--ignore-certificate-errors")
		}
	}

	chromeCaps := chrome.Capabilities{
		Args:            chromeArgs,
		ExcludeSwitches: []string{"enable-automation"},
		Prefs: map[string]interface{}{
			"profile.default_content_setting_values.notifications": 2,
		},
	}
	caps.AddChrome(chromeCaps)

	driver, err := selenium.NewRemote(caps, fmt.Sprintf("http://localhost:%d/wd/hub", port))
	if err != nil {
		return nil, fmt.Errorf("error creating WebDriver: %v", err)
	}
	defer driver.Quit()

	maskScript := `
        Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
        window.chrome = {runtime: {}};
        delete window.cdc_adoQpoasnfa76pfcZLmcfl_Array;
        delete window.cdc_adoQpoasnfa76pfcZLmcfl_Promise;
        delete window.cdc_adoQpoasnfa76pfcZLmcfl_Symbol;
    `

	driver.SetPageLoadTimeout(60 * time.Second)

	if err := driver.Get(url); err != nil {
		return nil, fmt.Errorf("navigation error: %w", err)
	}

	driver.ExecuteScript(maskScript, nil)

	time.Sleep(2 * time.Second)
	scrollScript := `
        window.scrollTo({
            top: Math.floor(Math.random() * document.body.scrollHeight / 2),
            behavior: 'smooth'
        });
    `
	driver.ExecuteScript(scrollScript, nil)
	time.Sleep(2 * time.Second)

	html, err := driver.PageSource()
	if err != nil {
		return nil, fmt.Errorf("page source error: %w", err)
	}

	return goquery.NewDocumentFromReader(strings.NewReader(html))
}
