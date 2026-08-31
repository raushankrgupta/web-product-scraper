package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
)

// DevStarsRequest is the body of POST /internal/stars.
type DevStarsRequest struct {
	// Action: grant | reset | forget | inspect
	Action string `json:"action"`
	// Email identifies the account. UserID is accepted as an alternative for
	// guest keys ("guest:<device>") which have no address.
	Email  string `json:"email"`
	UserID string `json:"user_id"`

	Stars       int    `json:"stars"`
	FreeCredits int    `json:"free_credits"`
	Note        string `json:"note"`
}

// DevStarsHandler is the test-only lever for the star economy: grant a
// balance, reset an account to brand-new, or forget an email so it counts as
// a first-time signup again.
//
// It exists because in-app purchases cannot complete on a sideloaded build —
// Play only sells to an app installed from a Play track. Without this, every
// paid path (Pro quality, couple, group, the 402 store flow, spend and refund)
// is untestable until an AAB is on an internal-testing track, which is a slow
// loop to be blocked behind.
//
// Registered only when ENVIRONMENT != "prod", and additionally requires the
// internal shared secret. Two independent gates, because an endpoint that
// mints currency is worth guarding twice: if this ever ran in production it
// would be a money printer.
func DevStarsHandler(w http.ResponseWriter, r *http.Request) {
	if config.IsProd() {
		http.NotFound(w, r)
		return
	}

	secret := config.InternalAPISecret
	given := r.Header.Get("X-Internal-Secret")
	if secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(given)) != 1 {
		utils.RespondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var req DevStarsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil {
		utils.RespondError(w, nil, "Invalid request body", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// "forget" works purely on the email hash and needs no account, so it is
	// handled before user resolution — the whole point is to run it after the
	// account has been deleted.
	if req.Action == "forget" {
		if strings.TrimSpace(req.Email) == "" {
			utils.RespondError(w, nil, "email is required for forget", http.StatusBadRequest)
			return
		}
		removed, err := utils.ForgetSignupIdentity(ctx, req.Email)
		if err != nil {
			utils.RespondError(w, nil, err.Error(), http.StatusInternalServerError)
			return
		}
		utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
			"action": "forget", "removed": removed,
			"note": "the next signup with this address counts as brand new",
		})
		return
	}

	userID, err := resolveDevUserID(ctx, req)
	if err != nil {
		utils.RespondError(w, nil, err.Error(), http.StatusNotFound)
		return
	}

	switch req.Action {
	case "grant":
		note := req.Note
		if note == "" {
			note = "dev grant"
		}
		if _, err := utils.AdjustStars(ctx, userID, req.Stars, req.FreeCredits, note); err != nil {
			utils.RespondError(w, nil, err.Error(), http.StatusInternalServerError)
			return
		}

	case "reset":
		if err := utils.ResetBalance(ctx, userID); err != nil {
			utils.RespondError(w, nil, err.Error(), http.StatusInternalServerError)
			return
		}

	case "inspect":
		// Nothing to do; the summary below is the answer.

	default:
		utils.RespondError(w, nil,
			"action must be one of: grant, reset, forget, inspect", http.StatusBadRequest)
		return
	}

	summary, err := utils.GetStarSummary(ctx, userID, strings.HasPrefix(userID, "guest:"))
	if err != nil {
		utils.RespondError(w, nil, err.Error(), http.StatusInternalServerError)
		return
	}

	out := map[string]interface{}{
		"action":  req.Action,
		"user_id": userID,
		"balance": summary,
	}
	if req.Email != "" {
		if id, ok := utils.LookupSignupIdentity(ctx, req.Email); ok {
			out["identity"] = map[string]interface{}{
				"signup_count":  id.SignupCount,
				"deleted_count": id.DeletedCount,
				"returning":     id.SignupCount > 1,
			}
		} else {
			out["identity"] = "none — this address counts as a new signup"
		}
	}
	utils.RespondJSON(w, http.StatusOK, out)
}

// resolveDevUserID maps an email (or an explicit id) onto a user id. Deleted
// accounts are matched on deleted_email too, so state can be inspected after
// a deletion test.
func resolveDevUserID(ctx context.Context, req DevStarsRequest) (string, error) {
	if id := strings.TrimSpace(req.UserID); id != "" {
		return id, nil
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		return "", errDevNeedsIdentifier
	}

	var user models.User
	err := utils.GetCollection(config.DBName, "users").FindOne(ctx, bson.M{
		"$or": bson.A{
			bson.M{"email": email},
			bson.M{"deleted_email": email},
		},
	}).Decode(&user)
	if err != nil {
		return "", errDevUserNotFound
	}
	return user.ID.Hex(), nil
}

var (
	errDevNeedsIdentifier = &devError{"email or user_id is required"}
	errDevUserNotFound    = &devError{"no account found for that email"}
)

type devError struct{ msg string }

func (e *devError) Error() string { return e.msg }
