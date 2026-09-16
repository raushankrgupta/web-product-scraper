package api

import (
	"net/http"
	"strings"
)

const (
	platformIOS     = "ios"
	platformAndroid = "android"
)

// requestPlatform is the client platform the app declares in X-Platform
// ("ios", "android", "web"), lower-cased; "" when absent.
//
// It is a hint about which store rules apply to the caller, never an
// authorisation input: a client can send anything. It is only used to
// withhold things (the review reward on iOS), and withholding something from a
// client that lies about being an iPhone costs nobody anything.
func requestPlatform(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.Header.Get("X-Platform")))
}
