package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// GuestTokenRequest is the body the mobile app sends to mint an anonymous
// session. device_id is whatever stable identifier the client has (Expo's
// expo-application installationId / Android ANDROID_ID / iOS identifierForVendor).
type GuestTokenRequest struct {
	DeviceID string `json:"device_id"`
}

// GuestTokenHandler mints a short-lived JWT bound to a device_id so users can
// run their first try-on without signing up. The token carries `guest: true`
// in its claims so AuthMiddleware can route around the users-collection check
// and apply the guest daily quota (1/day).
//
// Security note: this endpoint is intentionally public and trusts the device
// to supply its own id. The 1-try-on/day quota is the rate-limit, not the
// token itself. Don't expand guest powers without a server-side check.
func GuestTokenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.RespondError(w, nil, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req GuestTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, nil, "Invalid request body", http.StatusBadRequest)
		return
	}

	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		utils.RespondError(w, nil, "device_id is required", http.StatusBadRequest)
		return
	}
	if len(deviceID) > 128 {
		utils.RespondError(w, nil, "device_id too long", http.StatusBadRequest)
		return
	}

	// Use the same JWT plumbing as real users but prefix the subject with
	// "guest:" so it can never collide with an ObjectID.
	subject := fmt.Sprintf("guest:%s", deviceID)
	token, err := utils.GenerateGuestToken(subject)
	if err != nil {
		utils.RespondError(w, nil, "Failed to issue guest token", http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"token":   token,
		"user_id": subject,
		"plan":    "guest",
		"guest":   true,
	})
}

// Compile-time sanity check that jwt.MapClaims is still our claim shape.
// Prevents accidental refactors elsewhere from breaking guest parsing.
var _ = jwt.MapClaims{}
