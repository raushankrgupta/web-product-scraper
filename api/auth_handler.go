package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var googleOauthConfig *oauth2.Config

func init() {
	// So we will rely on a lazy initialization or just re-configure in the handler if needed,
	// but better to have a setup function or just use the config variables directly if they are populated.
	// However, init() runs before main(), so config variables will be empty here.
	// We will initialize the config inside the handlers or a helper function.
}

func getOauthConfig() *oauth2.Config {
	return &oauth2.Config{
		RedirectURL:  config.GoogleRedirectURL,
		ClientID:     config.GoogleClientID,
		ClientSecret: config.GoogleClientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile"},
		Endpoint:     google.Endpoint,
	}
}

// GoogleLoginHandler handles the login request by redirecting to Google
func GoogleLoginHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Google Login API]")

	oauthConfig := getOauthConfig()
	// State should be randomized for security in production
	url := oauthConfig.AuthCodeURL("random-state")

	utils.AddToLogMessage(&logMessageBuilder, "Redirecting to Google Auth")
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// GoogleCallbackHandler handles the callback from Google
func GoogleCallbackHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Google Callback API]")

	state := r.FormValue("state")
	if state != "random-state" {
		utils.AddToLogMessage(&logMessageBuilder, "Invalid state")
		http.Error(w, "State invalid", http.StatusBadRequest)
		return
	}

	code := r.FormValue("code")
	if code == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Code not found in callback")
		http.Error(w, "Code not found", http.StatusBadRequest)
		return
	}

	oauthConfig := getOauthConfig()
	token, err := oauthConfig.Exchange(context.Background(), code)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to exchange token: %v", err))
		http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := http.Get("https://www.googleapis.com/oauth2/v2/userinfo?access_token=" + token.AccessToken)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to get user info: %v", err))
		http.Error(w, "Failed to get user info: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to read user info response: %v", err))
		http.Error(w, "Failed to read user info: "+err.Error(), http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Successfully retrieved user info from Google")
	// For now, just return the user info as JSON
	w.Header().Set("Content-Type", "application/json")
	w.Write(content)
}
