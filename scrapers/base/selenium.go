package base

import (
	"fmt"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/tebeka/selenium"
	"github.com/tebeka/selenium/chrome"
)

const chromeDriverPath = "/usr/local/bin/chromedriver"

// FetchDocumentSelenium fetches the URL using Selenium and returns the page content as a string
func (b *BaseScraper) FetchDocumentSelenium(url string) (*goquery.Document, error) {
	// Initialize PortManager if not already
	InitPortManager(4444, 16)

	port, err := GlobalPortManager.GetPort()
	if err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}
	defer GlobalPortManager.ReleasePort(port)

	// Start ChromeDriver
	// Assuming chromedriver is in path
	opts := []selenium.ServiceOption{}
	// We can't easily start the service per request if we want speed, but for robustness we follow the pattern
	service, err := selenium.NewChromeDriverService(chromeDriverPath, port, opts...)
	if err != nil {
		return nil, fmt.Errorf("error starting Chrome driver service: %v", err)
	}
	defer service.Stop()

	// Caps
	caps := selenium.Capabilities{"browserName": "chrome"}

	// User Agent
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	chromeCaps := chrome.Capabilities{
		Args: []string{
			"--headless=new", // Use new headless
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--disable-blink-features=AutomationControlled",
			"--disable-extensions",
			"--disable-gpu",
			"--window-size=1920,1080",
			fmt.Sprintf("--user-agent=%s", userAgent),
		},
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

	// Anti-bot scripts
	maskScript := `
        Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
        window.chrome = {runtime: {}};
        delete window.cdc_adoQpoasnfa76pfcZLmcfl_Array;
        delete window.cdc_adoQpoasnfa76pfcZLmcfl_Promise;
        delete window.cdc_adoQpoasnfa76pfcZLmcfl_Symbol;
    `

	// Allow long load time
	driver.SetPageLoadTimeout(60 * time.Second)

	if err := driver.Get(url); err != nil {
		return nil, fmt.Errorf("navigation error: %w", err)
	}

	driver.ExecuteScript(maskScript, nil)

	// Human-like scroll
	time.Sleep(2 * time.Second)
	scrollScript := `
        window.scrollTo({
            top: Math.floor(Math.random() * document.body.scrollHeight / 2),
            behavior: 'smooth'
        });
    `
	driver.ExecuteScript(scrollScript, nil)
	time.Sleep(2 * time.Second) // wait for render

	html, err := driver.PageSource()
	if err != nil {
		return nil, fmt.Errorf("page source error: %w", err)
	}

	return goquery.NewDocumentFromReader(strings.NewReader(html))
}
