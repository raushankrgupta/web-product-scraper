package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// CollAIConsents records who agreed to third-party AI processing, and to
// which version of the disclosure.
const CollAIConsents = "ai_consents"

type aiConsentRequest struct {
	Version  int  `json:"version"`
	Accepted bool `json:"accepted"`
}

// AIConsentHandler serves POST /consent/ai.
//
// App Store guideline 5.1.2(i) requires explicit permission before personal
// data is shared with third-party AI. The app asks before the first
// generation; this stores the answer against the account so it can be shown
// to a reviewer or a user who asks, and so a change to the disclosure (a new
// version) can be re-asked.
//
// Deliberately a record, not a gate on the try-on endpoints: older app builds
// never send it, and refusing their generations would break every existing
// Android install on deploy. The app itself will not send a photo for
// generation without consent.
func AIConsentHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, nil, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req aiConsentRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil || req.Version <= 0 {
		utils.RespondError(w, nil, "Invalid request body", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	now := time.Now()
	_, err = utils.GetCollection(config.DBName, CollAIConsents).UpdateOne(ctx,
		bson.M{"_id": userID},
		bson.M{
			"$set": bson.M{
				"version":     req.Version,
				"accepted":    req.Accepted,
				"platform":    requestPlatform(r),
				"app_version": strings.TrimSpace(r.Header.Get("X-App-Version")),
				"is_guest":    IsGuestFromContext(r.Context()),
				"updated_at":  now,
			},
			"$setOnInsert": bson.M{"created_at": now},
		},
		options.Update().SetUpsert(true))
	if err != nil {
		utils.RespondInternalError(w, r, nil, "mongo",
			"Couldn't save your choice. Please try again.", err, http.StatusInternalServerError)
		return
	}
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{"version": req.Version, "accepted": req.Accepted})
}
