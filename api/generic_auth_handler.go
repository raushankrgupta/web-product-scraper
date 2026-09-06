package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/crypto/bcrypt"
)

// generateSecureOTP generates a cryptographically secure 6-digit numeric string
func generateSecureOTP() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		// Fallback should never happen with crypto/rand, but guarantee 6 digits
		return "482910"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// SignupRequest represents the payload for user registration
type SignupRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	DOB      string `json:"dob"`
	Gender   string `json:"gender"`
}

// LoginRequest represents the payload for user login
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// ForgotPasswordRequest represents the payload for forgot password
type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

// VerifyOTPRequest represents the payload for verifying OTP
type VerifyOTPRequest struct {
	Email string `json:"email"`
	OTP   string `json:"otp"`
	Mode  string `json:"mode,omitempty"`
}

// ResetPasswordRequest represents the payload for resetting password
type ResetPasswordRequest struct {
	Email       string `json:"email"`
	OTP         string `json:"otp"`
	NewPassword string `json:"new_password"`
}

// SignupHandler handles user registration
func SignupHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Signup API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Basic Validation
	if req.Name == "" || req.Email == "" || req.Password == "" {
		utils.RespondError(w, &logMessageBuilder, "Name, Email and Password are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if user already exists
	var existingUser models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&existingUser)
	if err == nil {
		if existingUser.Status == "deleted" {
			// User was deleted, rename the old email so it's free again.
			// Uses the same tombstone shape as DeleteAccountHandler and
			// GoogleLoginHandler so all three paths behave identically.
			newEmail := utils.TombstoneEmail(existingUser.Email, existingUser.ID)
			_, updateErr := collection.UpdateOne(ctx, bson.M{"_id": existingUser.ID},
				bson.M{"$set": bson.M{"email": newEmail, "deleted_email": existingUser.Email}})
			if updateErr != nil {
				utils.RespondError(w, &logMessageBuilder, "Failed to process previous account", http.StatusInternalServerError)
				return
			}
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Renamed deleted user email to %s", newEmail))
		} else {
			utils.RespondError(w, &logMessageBuilder, "User with this email already exists", http.StatusConflict)
			return
		}
	} else if err != mongo.ErrNoDocuments {
		utils.RespondError(w, &logMessageBuilder, "Database error checking user", http.StatusInternalServerError)
		return
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	// Generate secure OTP
	otpCode := generateSecureOTP()

	newUser := models.User{
		Name:         req.Name,
		Email:        req.Email,
		Password:     string(hashedPassword),
		DOB:          req.DOB,
		Gender:       req.Gender,
		Status:       "pending",
		OTP:          otpCode,
		OTPExpiresAt: time.Now().Add(10 * time.Minute),
		OTPAttempts:  0,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	res, err := collection.InsertOne(ctx, newUser)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to create user", http.StatusInternalServerError)
		return
	}

	// Send OTP Email
	emailErr := utils.SendEmail(req.Name, req.Email, "Verify your email",
		fmt.Sprintf("Your OTP is: %s", otpCode),
		fmt.Sprintf("<h1>Your OTP is: <strong>%s</strong></h1>", otpCode))

	if emailErr != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to send email: %v", emailErr))
		// Note: User created but email failed. Client might need to retry resend OTP.
	} else {
		utils.AddToLogMessage(&logMessageBuilder, "User registered successfully. Sent OTP email.")
	}

	newUser.ID = res.InsertedID.(primitive.ObjectID)
	newUser.Password = ""
	newUser.OTP = ""

	utils.RespondJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "User registered successfully. Please verify your email using the OTP sent.",
		"user":    newUser,
	})
}

// LoginHandler handles user login
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Login API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.Email == "" || req.Password == "" {
		utils.RespondError(w, &logMessageBuilder, "Email and Password are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.RespondError(w, &logMessageBuilder, "Invalid email or password", http.StatusUnauthorized)
		} else {
			utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
				"Something went wrong on our end. Please try again.", err, http.StatusInternalServerError)
		}
		return
	}

	// Compare password
	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password))
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid email or password", http.StatusUnauthorized)
		return
	}

	// Check status
	if user.Status == "deleted" {
		utils.RespondError(w, &logMessageBuilder, "Account deleted. Please sign up again to create a new account.", http.StatusForbidden)
		return
	}

	if user.Status == "pending" {
		utils.RespondError(w, &logMessageBuilder, "Please verify your email first", http.StatusForbidden)
		return
	}

	// Update status to active if verified
	if user.Status == "verified" {
		_, err := collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{"status": "active"}})
		if err != nil {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to update status to active: %v", err))
		} else {
			user.Status = "active"
			utils.AddToLogMessage(&logMessageBuilder, "User status updated to active")
		}
	}

	// Generate JWT Token
	token, err := utils.GenerateToken(user.ID.Hex())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	user.Password = ""
	utils.AddToLogMessage(&logMessageBuilder, "Login successful")
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Login successful",
		"token":   token,
		"user":    user,
	})
}

// VerifyOTPHandler handles OTP verification
// maxOTPAttempts caps how many times a single OTP may be guessed before it is
// burned and the user has to request a new one.
const maxOTPAttempts = 5

// consumeOTPAttempt reserves one verification attempt against the user's
// allowance, atomically. It reports false once the allowance is spent, in
// which case the caller must burn the OTP and refuse.
//
// The $exists arm is load-bearing. models.User tags otp_attempts `omitempty`,
// so SignupHandler's struct insert omits the field entirely when it is zero —
// and MongoDB's $lt does not match a missing field. A bare {$lt: 5} filter
// therefore matched nothing for every freshly signed-up user, locking them out
// of their own first verification attempt.
func consumeOTPAttempt(ctx context.Context, collection *mongo.Collection, userID primitive.ObjectID) (bool, error) {
	res, err := collection.UpdateOne(ctx,
		bson.M{
			"_id": userID,
			"$or": []bson.M{
				{"otp_attempts": bson.M{"$lt": maxOTPAttempts}},
				{"otp_attempts": bson.M{"$exists": false}},
			},
		},
		bson.M{"$inc": bson.M{"otp_attempts": 1}},
	)
	if err != nil {
		return false, err
	}
	return res.ModifiedCount > 0, nil
}

// burnOTP clears a spent OTP so a user who has exhausted their attempts has to
// start over from a freshly mailed code.
func burnOTP(ctx context.Context, collection *mongo.Collection, userID primitive.ObjectID) {
	_, _ = collection.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{
		"$unset": bson.M{"otp": "", "otp_expires_at": ""},
		"$set":   bson.M{"otp_attempts": 0},
	})
}

func VerifyOTPHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Verify OTP API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req VerifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.Email == "" || req.OTP == "" {
		utils.RespondError(w, &logMessageBuilder, "Email and OTP are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.RespondError(w, &logMessageBuilder, "User not found", http.StatusNotFound)
		} else {
			utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
				"Something went wrong on our end. Please try again.", err, http.StatusInternalServerError)
		}
		return
	}

	if user.OTP == "" {
		utils.RespondError(w, &logMessageBuilder, "No pending OTP found. Please request a new OTP.", http.StatusBadRequest)
		return
	}

	// Check if OTP has expired
	if !user.OTPExpiresAt.IsZero() && time.Now().After(user.OTPExpiresAt) {
		utils.RespondError(w, &logMessageBuilder, "OTP has expired. Please request a new OTP.", http.StatusUnauthorized)
		return
	}

	// Spend an attempt before comparing. Reserving first is what makes the
	// cap hold when two guesses race: both would otherwise read the same
	// pre-increment count and each conclude it was under the limit.
	allowed, err := consumeOTPAttempt(ctx, collection, user.ID)
	if err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
			"Something went wrong on our end. Please try again.", err, http.StatusInternalServerError)
		return
	}
	if !allowed {
		burnOTP(ctx, collection, user.ID)
		utils.RespondError(w, &logMessageBuilder, "Too many failed attempts. Please request a new OTP.", http.StatusTooManyRequests)
		return
	}

	// Constant-time OTP comparison to prevent timing attacks
	if subtle.ConstantTimeCompare([]byte(user.OTP), []byte(req.OTP)) != 1 {
		utils.RespondError(w, &logMessageBuilder, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	if user.Status == "verified" || user.Status == "active" {
		// If verified/active and OTP matches, we assume it's for Password Reset flow.
		// The OTP stays live for the reset call, so only the attempt counter is
		// cleared here — a correct guess must not count against the allowance.
		_, _ = collection.UpdateOne(ctx, bson.M{"_id": user.ID},
			bson.M{"$set": bson.M{"otp_attempts": 0}})
		utils.AddToLogMessage(&logMessageBuilder, "OTP verified for password reset")
		utils.RespondJSON(w, http.StatusOK, map[string]string{
			"message": "OTP verified successfully. Please proceed to reset password.",
		})
		return
	}

	// OTP Correct, verify user and clear OTP credentials
	update := bson.M{
		"$set":   bson.M{"status": "verified"},
		"$unset": bson.M{"otp": "", "otp_expires_at": "", "otp_attempts": ""},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update user status", http.StatusInternalServerError)
		return
	}

	user.Status = "verified"

	utils.AddToLogMessage(&logMessageBuilder, "OTP verified successfully")

	// The account only becomes usable here, so this is where the welcome
	// credits are issued. grantSignupBonus also records the email identity,
	// which is what downgrades the bonus for an address that has registered
	// before — deleting an account and signing up again is therefore worth
	// the returning grant, not a fresh full one.
	grantSignupBonus(user.ID.Hex(), user.Email, &logMessageBuilder)

	if req.Mode == "signup" {
		// Generate JWT Token
		token, err := utils.GenerateToken(user.ID.Hex())
		if err != nil {
			utils.RespondError(w, &logMessageBuilder, "Failed to generate token", http.StatusInternalServerError)
			return
		}

		user.Password = ""
		utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
			"message": "Email verified successfully",
			"token":   token,
			"user":    user,
		})
		return
	}

	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Email verification successful! You can now login.",
	})
}

// ForgotPasswordHandler handles forgot password requests
func ForgotPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Forgot Password API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.Email == "" {
		utils.RespondError(w, &logMessageBuilder, "Email is required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		// Return generic message to prevent account enumeration
		utils.AddToLogMessage(&logMessageBuilder, "User not found, returning generic response")
		utils.RespondJSON(w, http.StatusOK, map[string]string{
			"message": "If an account with that email exists, an OTP has been sent.",
		})
		return
	}

	// Generate secure OTP
	otpCode := generateSecureOTP()

	// Update User with OTP, 10-minute expiry, and reset attempts
	update := bson.M{
		"$set": bson.M{
			"otp":            otpCode,
			"otp_expires_at": time.Now().Add(10 * time.Minute),
			"otp_attempts":   0,
		},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to process request", http.StatusInternalServerError)
		return
	}

	// Send OTP Email
	emailErr := utils.SendEmail(user.Name, req.Email, "Reset Password OTP",
		fmt.Sprintf("Your OTP for password reset is: %s", otpCode),
		fmt.Sprintf("<h1>Your OTP for password reset is: <strong>%s</strong></h1>", otpCode))

	if emailErr != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to send email", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "OTP for password reset sent")
	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"message": "If an account with that email exists, an OTP has been sent.",
	})
}

// ResetPasswordHandler handles password reset with OTP
func ResetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Reset Password API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.Email == "" || req.OTP == "" || req.NewPassword == "" {
		utils.RespondError(w, &logMessageBuilder, "Email, OTP and New Password are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "User not found", http.StatusNotFound)
		return
	}

	if user.OTP == "" {
		utils.RespondError(w, &logMessageBuilder, "No active password reset request found", http.StatusBadRequest)
		return
	}

	// Check if OTP has expired
	if !user.OTPExpiresAt.IsZero() && time.Now().After(user.OTPExpiresAt) {
		utils.RespondError(w, &logMessageBuilder, "OTP has expired. Please request a new OTP.", http.StatusUnauthorized)
		return
	}

	// Same atomic allowance as VerifyOTPHandler: spend an attempt first, then
	// compare, so concurrent guesses cannot share one slot.
	allowed, err := consumeOTPAttempt(ctx, collection, user.ID)
	if err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
			"Something went wrong on our end. Please try again.", err, http.StatusInternalServerError)
		return
	}
	if !allowed {
		burnOTP(ctx, collection, user.ID)
		utils.RespondError(w, &logMessageBuilder, "Too many failed attempts. Please request a new OTP.", http.StatusTooManyRequests)
		return
	}

	if subtle.ConstantTimeCompare([]byte(user.OTP), []byte(req.OTP)) != 1 {
		utils.RespondError(w, &logMessageBuilder, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	// Hash new password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	// Update password and clear OTP credentials
	update := bson.M{
		"$set":   bson.M{"password": string(hashedPassword)},
		"$unset": bson.M{"otp": "", "otp_expires_at": "", "otp_attempts": ""},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update password", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Password reset successfully")
	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Password reset successfully. Please login with your new password.",
	})
}

// ChangePasswordRequest represents the payload for changing password
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePasswordHandler handles password change for logged-in users
func ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Change Password API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get User ID from Context
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID in context", http.StatusUnauthorized)
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.CurrentPassword == "" || req.NewPassword == "" {
		utils.RespondError(w, &logMessageBuilder, "Current and New Password are required", http.StatusBadRequest)
		return
	}

	// Fetch User
	objID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid user ID format", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err = collection.FindOne(ctx, bson.M{"_id": objID}).Decode(&user)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("User not found: %s", userID), http.StatusNotFound)
		return
	}

	// Verify Current Password
	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.CurrentPassword))
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid current password", http.StatusUnauthorized)
		return
	}

	// Hash New Password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to hash new password", http.StatusInternalServerError)
		return
	}

	// Update Password
	update := bson.M{
		"$set": bson.M{"password": string(hashedPassword)},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update password in DB", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Password changed successfully")
	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Password changed successfully",
	})
}

// DeleteAccountHandler handles soft deletion of user account
func DeleteAccountHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Delete Account API]")

	// DELETE is the documented verb, but POST is accepted as an alias:
	// several HTTP clients handle DELETE-with-body awkwardly. GET never
	// reaches here — DeleteAccountRoute sends it to the deletion page
	// instead — so it belongs in the Allow header even though this handler
	// does not serve it. MethodGuard rejects everything else upstream with a
	// logged 405 instead of a silent 401; this check is defence in depth for
	// anyone calling the handler directly.
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, DELETE, POST")
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get User ID from Context
	userIdStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID", http.StatusUnauthorized)
		return
	}
	userID, _ := primitive.ObjectIDFromHex(userIdStr)

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Look up the current email so we can free the address. Soft-deleting
	// while leaving `email` in place is what created a permanent lockout:
	// GoogleLoginHandler matches purely on email, finds the tombstone, and
	// returns "Account deleted. Please sign up again" — an instruction that
	// is impossible to follow, because signing up again hits the same row.
	var existing models.User
	if err := collection.FindOne(ctx, bson.M{"_id": userID}).Decode(&existing); err != nil {
		utils.RespondError(w, &logMessageBuilder, "User not found", http.StatusNotFound)
		return
	}

	// Capture why they are leaving before anything is purged. The body is
	// optional — the survey is skippable by design, and a user who declines
	// to answer must still be able to delete — so a missing or malformed
	// body is not an error here. It has to run before the balance is cleared
	// because the usage snapshot is what makes the answer interpretable.
	captureDeletionFeedback(ctx, r, userIdStr, existing.CreatedAt, &logMessageBuilder)

	// Record the deletion against the email identity and clear the star
	// balance before the address is tombstoned — `existing.Email` still holds
	// the real address at this point, and it is the input the identity hash
	// is derived from.
	releaseSignupIdentity(userIdStr, existing.Email, &logMessageBuilder)

	// Soft delete: status -> 'deleted', stamp deleted_at, and rename the
	// email to a tombstone address so the original is free for a genuine
	// re-signup (via Google *or* email/password) while the audit trail is
	// preserved in deleted_email. Matches what SignupHandler already did.
	update := bson.M{
		"$set": bson.M{
			"status":        "deleted",
			"deleted_at":    time.Now(),
			"email":         utils.TombstoneEmail(existing.Email, userID),
			"deleted_email": existing.Email,
		},
	}

	result, err := collection.UpdateOne(ctx, bson.M{"_id": userID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to delete account", http.StatusInternalServerError)
		return
	}

	if result.MatchedCount == 0 {
		utils.RespondError(w, &logMessageBuilder, "User not found", http.StatusNotFound)
		return
	}

	// Erase the account's owned content: S3 objects deleted, the documents
	// that referenced them soft-deleted. On its own context, because `ctx`
	// above is a 10s budget sized for a single update and this walks three
	// collections plus S3.
	//
	// A failure must not fail the deletion — the account is already
	// tombstoned and the user is entitled to that regardless. PurgeUserData
	// is idempotent, so the alert is a prompt to re-run it, not a dead end.
	purgeCtx, purgeCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer purgeCancel()
	if err := utils.PurgeUserData(purgeCtx, userIdStr); err != nil {
		alert.Errorf("privacy", "account data purge did not complete", err, "user_id", userIdStr)
	}

	utils.AddToLogMessage(&logMessageBuilder, "Account deleted successfully")
	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Account deleted successfully. You have been logged out.",
	})
}

// deleteAccountRequest is the optional exit survey sent with a deletion.
type deleteAccountRequest struct {
	Reason     string `json:"reason"`
	Details    string `json:"details"`
	AppVersion string `json:"app_version"`
}

// captureDeletionFeedback stores the exit survey, if one was sent.
//
// Every failure path here is silent on purpose. This is called from the
// deletion handler, and a user's right to delete their account cannot be
// contingent on a survey write succeeding — or on them having answered it at
// all. Anything that goes wrong is logged and the deletion continues.
func captureDeletionFeedback(ctx context.Context, r *http.Request, userID string,
	createdAt time.Time, logger *strings.Builder) {

	var req deleteAccountRequest
	body := http.MaxBytesReader(nil, r.Body, maxJSONBody)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return // no body, or not JSON: the survey was skipped
	}

	reason := strings.TrimSpace(req.Reason)
	details := strings.TrimSpace(req.Details)
	if reason == "" && details == "" {
		return
	}
	// Free text is user-authored and lands in a table someone will read.
	// Cap it so a paste of an entire log file doesn't become the document.
	if len(details) > 2000 {
		details = details[:2000]
	}

	ageDays := 0
	if !createdAt.IsZero() {
		ageDays = int(time.Since(createdAt).Hours() / 24)
	}

	utils.SaveDeletionFeedback(ctx, models.DeletionFeedback{
		UserID:         userID,
		Reason:         reason,
		Details:        details,
		AccountAgeDays: ageDays,
		AppVersion:     strings.TrimSpace(req.AppVersion),
	})
	utils.AddToLogMessage(logger, "deletion feedback recorded (reason: "+reason+")")
}

// GoogleLoginRequest represents the payload for Google Login
type GoogleLoginRequest struct {
	GoogleToken string `json:"google_token"`
}

// GoogleUserInfo represents the user info from Google
type GoogleUserInfo struct {
	Sub           string      `json:"sub"`
	Name          string      `json:"name"`
	GivenName     string      `json:"given_name"`
	FamilyName    string      `json:"family_name"`
	Picture       string      `json:"picture"`
	Email         string      `json:"email"`
	EmailVerified interface{} `json:"email_verified"`
	Locale        string      `json:"locale"`
	Aud           string      `json:"aud"`
	Azp           string      `json:"azp"`
}

// GoogleLoginHandler handles Google OAuth login
func GoogleLoginHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Google Login API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req GoogleLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.GoogleToken == "" {
		utils.RespondError(w, &logMessageBuilder, "Google token is required", http.StatusBadRequest)
		return
	}

	// Verify Google Token via google userinfo api
	// Works for access tokens
	userInfoReq, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to create request for Google API", http.StatusInternalServerError)
		return
	}
	userInfoReq.Header.Set("Authorization", "Bearer "+req.GoogleToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(userInfoReq)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}

		// Fallback: Check if it's an ID Token instead
		idTokenURL := "https://oauth2.googleapis.com/tokeninfo?id_token=" + url.QueryEscape(req.GoogleToken)
		idTokenResp, idErr := client.Get(idTokenURL)
		if idErr == nil && idTokenResp.StatusCode == http.StatusOK {
			resp = idTokenResp
		} else {
			if idTokenResp != nil {
				idTokenResp.Body.Close()
			}
			utils.RespondError(w, &logMessageBuilder, "Invalid Google token", http.StatusUnauthorized)
			return
		}
	}
	defer resp.Body.Close()

	var googleUser GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&googleUser); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to decode Google user info", http.StatusInternalServerError)
		return
	}

	if googleUser.Email == "" {
		utils.RespondError(w, &logMessageBuilder, "Email not provided by Google", http.StatusBadRequest)
		return
	}

	// If Aud/Azp wasn't provided by userinfo, fetch from tokeninfo
	if googleUser.Aud == "" && googleUser.Azp == "" {
		tokenInfoURL := "https://oauth2.googleapis.com/tokeninfo?id_token=" + url.QueryEscape(req.GoogleToken)
		tiResp, tiErr := client.Get(tokenInfoURL)
		if tiErr == nil && tiResp.StatusCode == http.StatusOK {
			var ti struct {
				Aud string `json:"aud"`
				Azp string `json:"azp"`
			}
			if err := json.NewDecoder(tiResp.Body).Decode(&ti); err == nil {
				googleUser.Aud = ti.Aud
				googleUser.Azp = ti.Azp
			}
			tiResp.Body.Close()
		}
	}

	// Verify audience against configured Google Client IDs to prevent account takeover
	allowedClients := make(map[string]bool)
	if config.GoogleClientID != "" {
		allowedClients[config.GoogleClientID] = true
	}
	if config.GoogleAndroidClientID != "" {
		allowedClients[config.GoogleAndroidClientID] = true
	}
	if config.GoogleIOSClientID != "" {
		allowedClients[config.GoogleIOSClientID] = true
	}

	if len(allowedClients) > 0 {
		audValid := allowedClients[googleUser.Aud] || allowedClients[googleUser.Azp]
		if !audValid {
			slog.Warn("google login rejected: aud mismatch",
				"received_aud", googleUser.Aud, "received_azp", googleUser.Azp)
			utils.RespondError(w, &logMessageBuilder, "Unauthorized: Invalid Google token audience", http.StatusUnauthorized)
			return
		}
	} else if config.IsProd() {
		slog.Error("google login rejected: GOOGLE_CLIENT_ID must be configured in production")
		utils.RespondError(w, &logMessageBuilder, "Google login is not properly configured on server", http.StatusInternalServerError)
		return
	}

	// Verify email is verified across both boolean and string representations
	emailVerified := false
	switch v := googleUser.EmailVerified.(type) {
	case bool:
		emailVerified = v
	case string:
		emailVerified = strings.ToLower(strings.TrimSpace(v)) == "true"
	default:
		emailVerified = false
	}
	if !emailVerified {
		utils.RespondError(w, &logMessageBuilder, "Google email is not verified", http.StatusUnauthorized)
		return
	}

	// Make sure name is populated
	name := googleUser.Name
	if name == "" {
		name = strings.Split(googleUser.Email, "@")[0]
	}

	collection := utils.GetCollection(config.DBName, "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err = collection.FindOne(ctx, bson.M{"email": googleUser.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// Register new user
			user = models.User{
				Name:      name,
				Email:     googleUser.Email,
				Status:    "active", // Google users are implicitly verified
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			res, insertErr := collection.InsertOne(ctx, user)
			if insertErr != nil {
				utils.RespondError(w, &logMessageBuilder, "Failed to register user", http.StatusInternalServerError)
				return
			}
			user.ID = res.InsertedID.(primitive.ObjectID)
			utils.AddToLogMessage(&logMessageBuilder, "New user registered via Google")
			grantSignupBonus(user.ID.Hex(), user.Email, &logMessageBuilder)
		} else {
			utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
				"Something went wrong on our end. Please try again.", err, http.StatusInternalServerError)
			return
		}
	} else {
		// Existing user.
		if user.Status == "deleted" {
			// A tombstone predating the delete-side fix still holds the
			// original address, which used to 403 this login forever. Free
			// the address now and fall through to creating a fresh account,
			// so the "sign up again" instruction actually works.
			freed := utils.TombstoneEmail(user.Email, user.ID)
			if _, updErr := collection.UpdateOne(ctx, bson.M{"_id": user.ID},
				bson.M{"$set": bson.M{"email": freed, "deleted_email": user.Email}}); updErr != nil {
				utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to free deleted email: %v", updErr))
				utils.RespondError(w, &logMessageBuilder, "Failed to process previous account", http.StatusInternalServerError)
				return
			}
			utils.AddToLogMessage(&logMessageBuilder, "Freed a legacy deleted-account email and re-registering")

			user = models.User{
				Name:      name,
				Email:     googleUser.Email,
				Status:    "active",
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			res, insertErr := collection.InsertOne(ctx, user)
			if insertErr != nil {
				utils.RespondError(w, &logMessageBuilder, "Failed to register user", http.StatusInternalServerError)
				return
			}
			user.ID = res.InsertedID.(primitive.ObjectID)
			// A fresh account over a deleted one. The identity record
			// survived the deletion, so this grant is the smaller returning
			// one rather than the full welcome bonus.
			grantSignupBonus(user.ID.Hex(), user.Email, &logMessageBuilder)
		} else if user.Status == "pending" {
			// If they were pending, Google login verifies them
			if _, err := collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{"status": "active"}}); err != nil {
				utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to update status to active: %v", err))
			} else {
				user.Status = "active"
			}
		}
	}

	// Generate JWT Token
	token, err := utils.GenerateToken(user.ID.Hex())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	user.Password = "" // Hide password in response

	utils.AddToLogMessage(&logMessageBuilder, "Google Login successful")
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Login successful",
		"token":   token,
		"user":    user,
	})
}
