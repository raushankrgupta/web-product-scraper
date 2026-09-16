package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// submitApplePurchase is the iOS branch of POST /billing/purchase.
//
// Status codes matter more than usual here, because the app decides whether
// to finish the StoreKit transaction from them: a finished transaction is never
// delivered again. So anything that is our fault or Apple's (not configured,
// Apple unreachable) is a 5xx and leaves the transaction open to retry, and
// only a definitive answer about the purchase itself is a 2xx.
func submitApplePurchase(w http.ResponseWriter, r *http.Request, userID string, req PurchaseRequest) {
	if req.TransactionID == "" && req.PurchaseToken == "" {
		utils.RespondError(w, nil, "transaction_id or purchase_token is required", http.StatusBadRequest)
		return
	}
	if !utils.AppStoreConfigured() {
		utils.RespondInternalError(w, r, nil, "billing",
			"Purchases are temporarily unavailable. If you were charged, your stars will be added automatically.",
			errors.New("app store server api not configured"), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := utils.SubmitApplePurchase(ctx, userID, utils.ApplePurchaseInput{
		ProductID:         req.ProductID,
		TransactionID:     req.TransactionID,
		SignedTransaction: req.PurchaseToken,
	})
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
	case errors.Is(err, utils.ErrApplePurchaseInvalid):
		// Definitive: this transaction will never be a star purchase. 200 with
		// a rejected state lets the app close it out.
		utils.RespondJSON(w, http.StatusOK, map[string]interface{}{"result": result, "balance": nil})
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

// AppleNotificationsHandler receives App Store Server Notifications V2.
//
// The iOS counterpart of PlayRTDNHandler, and needed for the same two reasons:
// a purchase whose transaction never reached /billing/purchase still has to be
// credited, and a refund has to take the stars back.
//
// There is no shared secret on the URL, because none is needed: the body is a
// JWS signed by Apple and verified against Apple's root certificate. A forged
// notification fails that check and changes nothing.
//
// Apple retries anything that is not a 200, so a transient failure answers 500
// and a notification that can never be processed answers 200.
func AppleNotificationsHandler(w http.ResponseWriter, r *http.Request) {
	log := utils.L(r.Context())

	var body struct {
		SignedPayload string `json:"signedPayload"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&body); err != nil || body.SignedPayload == "" {
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "unparseable"})
		return
	}

	note, err := utils.DecodeAppleNotification(body.SignedPayload)
	if err != nil {
		log.Warn("rejected apple notification", "error", err.Error())
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "invalid"})
		return
	}

	if note.Data.BundleID != "" && note.Data.BundleID != config.AppleBundleID {
		log.Warn("apple notification for an unexpected bundle", "bundle", note.Data.BundleID)
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	if note.Data.Environment == utils.AppleEnvSandbox && !config.AppStoreAllowSandbox {
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "sandbox-ignored"})
		return
	}

	log.Info("apple notification",
		"type", note.NotificationType, "subtype", note.Subtype,
		"uuid", note.NotificationUUID, "environment", note.Data.Environment)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	txn := note.Transaction
	switch note.NotificationType {
	case "TEST":
		utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "test-ok"})
		return

	case "ONE_TIME_CHARGE":
		// A consumable was bought. Normally the app has already submitted it
		// and this is a no-op; when the app died before submitting, this is
		// the only thing that credits the person who paid.
		if txn == nil {
			break
		}
		userID, ok := utils.UserForAppleTransaction(ctx, txn)
		if !ok {
			log.Warn("apple purchase with no attributable account", "product", txn.ProductID)
			break
		}
		if _, err := utils.SubmitApplePurchase(ctx, userID, utils.ApplePurchaseInput{
			ProductID: txn.ProductID, TransactionID: txn.TransactionID,
		}); err != nil && !errors.Is(err, utils.ErrApplePurchaseInvalid) &&
			!errors.Is(err, utils.ErrUnknownProduct) {
			log.Error("apple notification credit failed", "error", err.Error())
			utils.RespondError(w, nil, "retry", http.StatusInternalServerError)
			return
		}

	case "REFUND":
		if txn == nil {
			break
		}
		if err := utils.RevokePurchase(ctx, utils.ApplePurchaseKey(txn.TransactionID), "refunded at Apple"); err != nil {
			log.Error("apple refund revoke failed", "error", err.Error())
			utils.RespondError(w, nil, "retry", http.StatusInternalServerError)
			return
		}

	case "REFUND_REVERSED":
		if txn == nil {
			break
		}
		if err := utils.RestoreRevokedPurchase(ctx, utils.ApplePurchaseKey(txn.TransactionID), "refund reversed at Apple"); err != nil {
			log.Error("apple refund reversal failed", "error", err.Error())
			utils.RespondError(w, nil, "retry", http.StatusInternalServerError)
			return
		}

	case "CONSUMPTION_REQUEST":
		// The customer asked Apple for a refund and Apple is inviting
		// consumption data to inform its decision. Responding is optional;
		// the outcome arrives later as REFUND (or nothing).
	}

	utils.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
