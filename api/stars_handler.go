package api

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// CatalogHandler returns the star catalogue: what a pack costs, what a
// generation costs, and what each quality tier is called.
//
// The app renders prices from this rather than from a hardcoded table, so a
// repricing in config/stars.json reaches users on the next app launch instead
// of requiring a store release. A client that has cached an old catalogue can
// still only ever be wrong about what it *displays* — the charge itself is
// computed server-side from the same config.
func CatalogHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Stars

	type qualityView struct {
		Key     string `json:"key"`
		Label   string `json:"label"`
		Tagline string `json:"tagline"`
	}

	qualities := make([]qualityView, 0, len(cfg.Models))
	// Emit the default quality first so the app's selector defaults to the
	// cheap tier without needing to know its name.
	order := []string{cfg.DefaultQuality}
	for k := range cfg.Models {
		if k != cfg.DefaultQuality {
			order = append(order, k)
		}
	}
	for _, k := range order {
		m, ok := cfg.Model(k)
		if !ok {
			continue
		}
		qualities = append(qualities, qualityView{Key: k, Label: m.Label, Tagline: m.Tagline})
	}

	utils.RespondJSONWithETag(w, r, http.StatusOK, map[string]interface{}{
		"version":         cfg.Version,
		"currency":        cfg.Currency,
		"packs":           cfg.SortedPacks(),
		"tiers":           cfg.Tiers,
		"qualities":       qualities,
		"default_quality": cfg.DefaultQuality,
		"free": map[string]interface{}{
			"welcome_credits":  cfg.Free.WelcomeCredits,
			"daily_free_count": cfg.Free.DailyFreeCount,
			"free_quality":     cfg.Free.FreeQuality,
			"free_types":       cfg.Free.FreeTypes,
			"stars_threshold":  cfg.CheapestTierStars(),
		},
	})
}

// PurchaseRequest is the body of POST /billing/purchase.
type PurchaseRequest struct {
	ProductID string `json:"product_id"`
	// PurchaseToken comes from the Play Billing library on the device. It is
	// a claim, not proof — the server verifies it with Google before any
	// stars move.
	PurchaseToken string `json:"purchase_token"`
}

// PurchaseHandler credits a completed Google Play purchase.
//
// Everything that decides whether stars are granted comes from Google, never
// from the request body: the client supplies a token, and the server asks
// Play whether that token represents a real, completed purchase of a product
// we sell. Trusting the client here would mean anyone able to POST could mint
// stars for free.
func PurchaseHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, nil, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if IsGuestFromContext(r.Context()) {
		// A guest has no durable account to attach a purchase to; crediting
		// one would strand the stars on a device-scoped identity.
		utils.RespondJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":      "Please create an account before buying stars.",
			"signup_cta": true,
		})
		return
	}

	var req PurchaseRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil {
		utils.RespondError(w, nil, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ProductID == "" || req.PurchaseToken == "" {
		utils.RespondError(w, nil, "product_id and purchase_token are required", http.StatusBadRequest)
		return
	}

	if !utils.PlayBillingConfigured() {
		utils.RespondInternalError(w, r, nil, "billing",
			"Purchases are temporarily unavailable. You have not been charged for stars — "+
				"if money left your account, it will be refunded automatically.",
			errors.New("play service account not configured"), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := utils.SubmitPurchase(ctx, userID, req.ProductID, req.PurchaseToken)
	switch {
	case errors.Is(err, utils.ErrUnknownProduct):
		utils.RespondError(w, nil, "Unknown product.", http.StatusBadRequest)
		return
	case errors.Is(err, utils.ErrTokenBelongsToAnotherUser):
		utils.RespondError(w, nil, "That purchase is already linked to a different account.", http.StatusConflict)
		return
	case errors.Is(err, utils.ErrPurchaseNotFound):
		utils.RespondError(w, nil, "We couldn't find that purchase. If you were charged, it will appear shortly.", http.StatusNotFound)
		return
	case err != nil:
		utils.RespondInternalError(w, r, nil, "billing",
			"We couldn't confirm that purchase. If you were charged, your stars will be added automatically.",
			err, http.StatusInternalServerError)
		return
	}

	summary, sumErr := utils.GetStarSummary(ctx, userID, false)
	if sumErr != nil {
		utils.RespondInternalError(w, r, nil, "stars",
			"Your purchase went through, but we couldn't load your balance. Reopen the app to see it.",
			sumErr, http.StatusInternalServerError)
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"result":  result,
		"balance": summary,
	})
}

// LedgerHandler returns a user's star transaction history, newest first.
// Having it in the app is what stops "where did my stars go" turning into a
// support email — the user can see every grant, spend and refund themselves.
func LedgerHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, nil, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	entries, err := utils.ListLedger(ctx, userID, limit)
	if err != nil {
		utils.RespondInternalError(w, r, nil, "stars",
			"Couldn't load your star history. Please try again.", err, http.StatusInternalServerError)
		return
	}
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

// ---------------------------------------------------- Play RTDN push webhook

// pubSubPush is the envelope Google Pub/Sub posts to a push endpoint.
type pubSubPush struct {
	Message struct {
		Data      string `json:"data"` // base64 DeveloperNotification
		MessageID string `json:"messageId"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// developerNotification is Play's Real-time Developer Notification payload.
type developerNotification struct {
	Version     string `json:"version"`
	PackageName string `json:"packageName"`

	OneTimeProductNotification *struct {
		NotificationType int    `json:"notificationType"` // 1 purchased, 2 cancelled
		PurchaseToken    string `json:"purchaseToken"`
		SKU              string `json:"sku"`
	} `json:"oneTimeProductNotification"`

	VoidedPurchaseNotification *struct {
		PurchaseToken string `json:"purchaseToken"`
		OrderID       string `json:"orderId"`
		RefundType    int    `json:"refundType"`
	} `json:"voidedPurchaseNotification"`

	TestNotification *struct {
		Version string `json:"version"`
	} `json:"testNotification"`
}

const (
	oneTimePurchased = 1
	oneTimeCancelled = 2
)

// PlayRTDNHandler receives Real-time Developer Notifications from Google Play
// via a Pub/Sub push subscription.
//
// This is what makes two things work that the request path cannot:
//
//	Pending payments. A UPI or netbanking purchase in India completes minutes
//	after the app has moved on. The purchase notification is how those stars
//	ever get credited.
//
//	Refunds and chargebacks. Without them a user can buy stars, spend them,
//	refund the payment and keep the images — repeatedly.
//
// The endpoint is unauthenticated in the usual sense (Google is the caller),
// so it is guarded by a shared token in the query string. Without that, anyone
// who discovers the URL can forge a refund and zero out a user's balance.
func PlayRTDNHandler(w http.ResponseWriter, r *http.Request) {
	if config.PlayRTDNToken == "" {
		// Refusing is the safe default: an open endpoint that mutates
		// balances is worse than one that is switched off.
		utils.RespondError(w, nil, "Not configured", http.StatusServiceUnavailable)
		return
	}
	got := r.URL.Query().Get("token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(config.PlayRTDNToken)) != 1 {
		utils.L(r.Context()).Warn("rejected play rtdn push with a bad token")
		utils.RespondError(w, nil, "Forbidden", http.StatusForbidden)
		return
	}

	var push pubSubPush
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&push); err != nil {
		// A malformed body will never become valid, so acknowledge it rather
		// than letting Pub/Sub redeliver it forever.
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "unparseable"})
		return
	}

	raw, err := base64.StdEncoding.DecodeString(push.Message.Data)
	if err != nil {
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "undecodable"})
		return
	}

	var note developerNotification
	if err := json.Unmarshal(raw, &note); err != nil {
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "unparseable"})
		return
	}

	log := utils.L(r.Context())

	if note.TestNotification != nil {
		log.Info("play rtdn test notification received", "package", note.PackageName)
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "test-ok"})
		return
	}

	// Reject notifications for a different app outright — a shared Pub/Sub
	// topic should never be able to move our balances.
	if note.PackageName != "" && note.PackageName != config.Stars.Billing.PackageName {
		log.Warn("play rtdn for an unexpected package", "package", note.PackageName)
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch {
	case note.VoidedPurchaseNotification != nil:
		v := note.VoidedPurchaseNotification
		if err := utils.RevokePurchase(ctx, v.PurchaseToken, "voided (rtdn)"); err != nil {
			log.Error("rtdn revoke failed", "error", err.Error())
			// 500 makes Pub/Sub retry, which is what we want for a
			// transient failure — an unreconciled refund is money lost.
			utils.RespondError(w, nil, "retry", http.StatusInternalServerError)
			return
		}

	case note.OneTimeProductNotification != nil:
		n := note.OneTimeProductNotification
		switch n.NotificationType {
		case oneTimePurchased:
			// Attribute via the purchase we recorded when the client first
			// submitted the token. Without that record we cannot know whose
			// balance to credit, and guessing is not an option.
			userID, ok := utils.UserForPurchaseToken(ctx, n.PurchaseToken)
			if !ok {
				log.Warn("rtdn purchase for an unknown token — cannot attribute",
					"sku", n.SKU)
				break
			}
			if _, err := utils.SubmitPurchase(ctx, userID, n.SKU, n.PurchaseToken); err != nil {
				log.Error("rtdn purchase credit failed", "error", err.Error())
				utils.RespondError(w, nil, "retry", http.StatusInternalServerError)
				return
			}
		case oneTimeCancelled:
			if err := utils.RevokePurchase(ctx, n.PurchaseToken, "cancelled (rtdn)"); err != nil {
				log.Error("rtdn cancel failed", "error", err.Error())
				utils.RespondError(w, nil, "retry", http.StatusInternalServerError)
				return
			}
		}
	}

	utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
