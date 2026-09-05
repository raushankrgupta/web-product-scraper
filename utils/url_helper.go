package utils

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// trackingParams are query keys that identify the *referral*, never the
// product. Stripping them fixes a real dedup problem visible in production:
// the same Meesho product arrived twice with different utm_source values and
// was treated as two distinct scrapes.
//
// Note what is NOT here: `variant`. On Shopify stores that parameter selects
// the actual SKU, so dropping it would silently scrape the wrong colour or
// size. Only this allow-list is removed — never "all query params".
var trackingParams = map[string]bool{
	"gclid": true, "fbclid": true, "srsltid": true, "igshid": true,
	"mc_cid": true, "mc_eid": true, "ref_src": true, "_branch_match_id": true,
}

var _, cgnatNet, _ = net.ParseCIDR("100.64.0.0/10")

// IsRestrictedIP reports whether an IP address belongs to a private, loopback,
// link-local (including cloud metadata), carrier-grade NAT, or unspecified range.
func IsRestrictedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		if v4.IsLoopback() || v4.IsPrivate() || v4.IsLinkLocalUnicast() || v4.IsLinkLocalMulticast() || v4.IsUnspecified() || v4.IsMulticast() {
			return true
		}
		if cgnatNet != nil && cgnatNet.Contains(v4) {
			return true
		}
		return false
	}
	// IPv6
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// ValidateSafeURL checks whether a URL is safe against SSRF (Server-Side Request Forgery).
// It rejects unsupported schemes, localhost/metadata hostnames, and any host resolving to a restricted IP.
func ValidateSafeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported url scheme %q: only http and https are allowed", u.Scheme)
	}

	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return errors.New("url has empty host")
	}

	// Suffix matches, not substring: `strings.Contains(host, "metadata")`
	// also rejects legitimate retailers, and the numeric metadata addresses
	// are covered by IsRestrictedIP once the host resolves.
	lowerHost := strings.ToLower(host)
	if lowerHost == "localhost" ||
		strings.HasSuffix(lowerHost, ".localhost") ||
		strings.HasSuffix(lowerHost, ".internal") ||
		strings.HasSuffix(lowerHost, ".local") ||
		lowerHost == "metadata" ||
		lowerHost == "metadata.google.internal" {
		return fmt.Errorf("access to internal/metadata host %q is blocked", host)
	}

	// If host is a literal IP, check directly
	if ip := net.ParseIP(host); ip != nil {
		if IsRestrictedIP(ip) {
			return fmt.Errorf("access to restricted IP %s is blocked", host)
		}
		return nil
	}

	// Resolve hostname via DNS. Bounded: this runs on the request path, and
	// net.LookupIP takes no context — a slow resolver would otherwise pin the
	// handler for as long as the system resolver felt like taking.
	resolveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
	if err != nil {
		return fmt.Errorf("failed to resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q did not resolve to any IP address", host)
	}

	for _, addr := range ips {
		if IsRestrictedIP(addr.IP) {
			return fmt.Errorf("host %q resolved to restricted IP %s", host, addr.IP.String())
		}
	}

	return nil
}

// SafeDialerControl returns a Control hook for net.Dialer that prevents connecting
// to loopback, private, link-local (cloud metadata), or CGNAT IP addresses.
func SafeDialerControl(network, address string, c syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if IsRestrictedIP(ip) {
		return fmt.Errorf("SSRF blocked: connection to restricted IP %s is prohibited", host)
	}
	return nil
}

// NewSafeTransport creates an http.Transport with anti-SSRF dialer controls.
func NewSafeTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   SafeDialerControl,
	}

	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// NewSafeHTTPClient returns an http.Client equipped with SSRF defenses on both
// DNS resolution and redirect chains.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: NewSafeTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if err := ValidateSafeURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}

// NormalizeProductURL cleans a user-pasted product URL.
func NormalizeProductURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	// Cut at the first internal whitespace — "look at this <url> nice na?"
	if i := strings.IndexAny(s, " \t\r\n"); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return "", fmt.Errorf("empty url")
	}

	// A scheme-less paste ("www.meesho.com/...") is still a URL to a user.
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("url has no host: %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported url scheme %q", u.Scheme)
	}

	// Enforce anti-SSRF checks on normalized URL
	if err := ValidateSafeURL(u.String()); err != nil {
		return "", err
	}

	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || strings.HasPrefix(lk, "gad_") || trackingParams[lk] {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""

	return u.String(), nil
}

// ResolveShortenedURL follows redirects to find the final URL with anti-SSRF enforcement
func ResolveShortenedURL(rawURL string) (string, error) {
	if err := ValidateSafeURL(rawURL); err != nil {
		return rawURL, err
	}

	client := NewSafeHTTPClient(30 * time.Second)

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return rawURL, err
	}

	// Mimic a real browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := client.Do(req)
	if err != nil {
		return rawURL, err
	}
	defer resp.Body.Close()

	finalURL := resp.Request.URL.String()
	if err := ValidateSafeURL(finalURL); err != nil {
		return rawURL, fmt.Errorf("final resolved URL blocked by SSRF policy: %w", err)
	}

	return finalURL, nil
}
