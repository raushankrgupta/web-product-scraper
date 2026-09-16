package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
)

// AppleLoginRequest is the body of POST /auth/apple, straight from
// expo-apple-authentication's credential.
type AppleLoginRequest struct {
	IdentityToken     string `json:"identity_token"`
	AuthorizationCode string `json:"authorization_code"`
	// Apple sends the name only the first time a user authorises the app, and
	// only to the device — never in the token. It is saved on that first call
	// because there will not be a second chance.
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

// AppleLoginHandler signs a user in with Apple, creating the account on first
// use. App Store guideline 4.8 requires this whenever Google Sign-In is
// offered.
//
// Accounts are matched in this order:
//
//  1. apple_sub — the stable id for this Apple ID in this app.
//  2. A verified email equal to an existing account's — the same person who
//     signed up with Google or a password, now using Apple. Safe to link
//     because Apple has verified they own that address. A private relay
//     address never matches an existing account, so it never links.
//  3. Otherwise a new account.
func AppleLoginHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Apple Login API]")

	var req AppleLoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.IdentityToken) == "" {
		utils.RespondError(w, &logMessageBuilder, "identity_token is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	identity, err := utils.VerifyAppleIdentityToken(ctx, req.IdentityToken)
	if err != nil {
		if errors.Is(err, utils.ErrAppleIdentityInvalid) {
			utils.AddToLogMessage(&logMessageBuilder, "rejected: "+err.Error())
			utils.RespondError(w, &logMessageBuilder, "Invalid Apple sign-in. Please try again.", http.StatusUnauthorized)
			return
		}
		utils.RespondInternalError(w, r, &logMessageBuilder, "auth",
			"Couldn't reach Apple to confirm your sign-in. Please try again.", err, http.StatusBadGateway)
		return
	}

	users := utils.GetCollection(config.DBName, "users")
	email := strings.ToLower(identity.Email)

	var user models.User
	found := false

	err = users.FindOne(ctx, bson.M{"apple_sub": identity.Subject, "status": bson.M{"$ne": "deleted"}}).Decode(&user)
	switch {
	case err == nil:
		found = true
	case err != mongo.ErrNoDocuments:
		utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
			"Something went wrong on our end. Please try again.", err, http.StatusInternalServerError)
		return
	}

	if !found && email != "" && identity.EmailVerified {
		// Case-insensitive: older rows store addresses exactly as typed.
		err = users.FindOne(ctx, bson.M{"email": primitive.Regex{
			Pattern: "^" + regexp.QuoteMeta(email) + "$", Options: "i",
		}}).Decode(&user)
		switch {
		case err == nil && user.Status == "deleted":
			// A legacy tombstone still holding the address — free it, exactly
			// as GoogleLoginHandler does, and fall through to a new account.
			if _, updErr := users.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{
				"email": utils.TombstoneEmail(user.Email, user.ID), "deleted_email": user.Email,
			}}); updErr != nil {
				utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
					"Something went wrong on our end. Please try again.", updErr, http.StatusInternalServerError)
				return
			}
			user = models.User{}
		case err == nil:
			found = true
			set := bson.M{"apple_sub": identity.Subject, "updated_at": time.Now()}
			if user.Status == "pending" {
				// Apple has verified the address, which is what the OTP was for.
				set["status"] = "active"
				user.Status = "active"
			}
			if _, updErr := users.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": set}); updErr != nil {
				utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
					"Something went wrong on our end. Please try again.", updErr, http.StatusInternalServerError)
				return
			}
			utils.AddToLogMessage(&logMessageBuilder, "linked Apple sign-in to an existing account")
		case err != mongo.ErrNoDocuments:
			utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
				"Something went wrong on our end. Please try again.", err, http.StatusInternalServerError)
			return
		}
	}

	isNew := false
	if !found {
		if email == "" {
			// Apple omits the email when the user never shared one with this
			// app. The email column is unique and every identity check keys on
			// it, so a stable per-subject placeholder stands in.
			email = appleFallbackEmail(identity.Subject)
		}
		user = models.User{
			Name:      appleDisplayName(req.GivenName, req.FamilyName, email),
			Email:     email,
			Status:    "active",
			AppleSub:  identity.Subject,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		res, insertErr := users.InsertOne(ctx, user)
		if insertErr != nil {
			if mongo.IsDuplicateKeyError(insertErr) {
				// Two first sign-ins raced; the other one created the account.
				utils.RespondError(w, &logMessageBuilder, "Please try signing in again.", http.StatusConflict)
				return
			}
			utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
				"Failed to create your account. Please try again.", insertErr, http.StatusInternalServerError)
			return
		}
		user.ID = res.InsertedID.(primitive.ObjectID)
		isNew = true
		utils.AddToLogMessage(&logMessageBuilder, "New user registered via Apple")
	} else if user.Name == "" || strings.HasPrefix(user.Name, "Apple user") {
		if name := appleDisplayName(req.GivenName, req.FamilyName, ""); name != "" {
			_, _ = users.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{"name": name}})
			user.Name = name
		}
	}

	// Keep a refresh token so deleting the account can revoke the grant.
	// Sign-in must not fail because of it: without the key configured, or if
	// Apple is slow, the user is still who the identity token says they are.
	if code := strings.TrimSpace(req.AuthorizationCode); code != "" && utils.AppleSignInRevokeConfigured() {
		if refresh, exErr := utils.ExchangeAppleAuthCode(ctx, code); exErr != nil {
			utils.AddToLogMessage(&logMessageBuilder, "apple code exchange failed: "+exErr.Error())
		} else if _, updErr := users.UpdateOne(ctx, bson.M{"_id": user.ID},
			bson.M{"$set": bson.M{"apple_refresh_token": refresh}}); updErr != nil {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("failed to store apple refresh token: %v", updErr))
		}
	}

	if isNew {
		grantSignupBonus(user.ID.Hex(), user.Email, &logMessageBuilder)
	}

	token, err := utils.GenerateToken(user.ID.Hex())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	user.Password = ""

	utils.AddToLogMessage(&logMessageBuilder, "Apple Login successful")
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Login successful",
		"token":   token,
		"user":    user,
		"is_new":  isNew,
	})
}

func appleDisplayName(given, family, email string) string {
	name := strings.TrimSpace(strings.TrimSpace(given) + " " + strings.TrimSpace(family))
	if name != "" {
		return name
	}
	if email != "" && !strings.HasSuffix(email, "@privaterelay.appleid.com") && !strings.HasSuffix(email, appleFallbackDomain) {
		return strings.Split(email, "@")[0]
	}
	if email == "" {
		return ""
	}
	return "Apple user"
}

const appleFallbackDomain = "@apple-signin.invalid"

func appleFallbackEmail(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return "apple+" + hex.EncodeToString(sum[:8]) + appleFallbackDomain
}

// revokeAppleSignIn is called when an account is deleted. A failure is alerted
// and does not block the deletion: the user's right to delete comes first, and
// they can still remove the app from Settings → Apple ID → Sign in with Apple.
func revokeAppleSignIn(user models.User, logger *strings.Builder) {
	if user.AppleRefreshToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := utils.RevokeAppleToken(ctx, user.AppleRefreshToken); err != nil {
		alert.Errorf("privacy", "sign in with apple token revoke failed on account deletion", err,
			"user_id", user.ID.Hex())
		utils.AddToLogMessage(logger, "apple token revoke failed: "+err.Error())
		return
	}
	utils.AddToLogMessage(logger, "apple token revoked")
}
