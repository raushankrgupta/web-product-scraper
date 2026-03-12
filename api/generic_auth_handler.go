package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/crypto/bcrypt"
)

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
// SignupHandler handles user registration
func SignupHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
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

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if user already exists
	var existingUser models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&existingUser)
	if err == nil {
		if existingUser.Status == "deleted" {
			// User was deleted, rename old email to allow re-signup
			newEmail := fmt.Sprintf("deleted_%d_%s", time.Now().Unix(), req.Email)
			_, updateErr := collection.UpdateOne(ctx, bson.M{"_id": existingUser.ID}, bson.M{"$set": bson.M{"email": newEmail}})
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

	// Generate OTP
	otpCode := ""
	for i := 0; i < 6; i++ {
		b := make([]byte, 1)
		rand.Read(b)
		otpCode += fmt.Sprintf("%d", int(b[0])%10)
	}

	newUser := models.User{
		Name:      req.Name,
		Email:     req.Email,
		Password:  string(hashedPassword),
		DOB:       req.DOB,
		Gender:    req.Gender,
		Status:    "pending",
		OTP:       otpCode,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
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

	utils.RespondJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "User registered successfully. Please verify your email using the OTP sent.",
		"user":    newUser,
	})
}

// LoginHandler handles user login
// LoginHandler handles user login
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
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

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.RespondError(w, &logMessageBuilder, "Invalid email or password", http.StatusUnauthorized)
		} else {
			utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
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

	utils.AddToLogMessage(&logMessageBuilder, "Login successful")
	utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Login successful",
		"token":   token,
		"user":    user,
	})
}

// VerifyOTPHandler handles OTP verification
// VerifyOTPHandler handles OTP verification
func VerifyOTPHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
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

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.RespondError(w, &logMessageBuilder, "User not found", http.StatusNotFound)
		} else {
			utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		}
		return
	}

	if user.Status == "verified" || user.Status == "active" {
		if user.OTP != req.OTP {
			utils.RespondError(w, &logMessageBuilder, "Invalid OTP", http.StatusUnauthorized)
			return
		}
		// If verified/active and OTP matches, we assume it's for Password Reset flow.
		utils.AddToLogMessage(&logMessageBuilder, "OTP verified for password reset")
		utils.RespondJSON(w, http.StatusOK, map[string]string{
			"message": "OTP verified successfully. Please proceed to reset password.",
		})
		return
	}

	if user.OTP != req.OTP {
		utils.RespondError(w, &logMessageBuilder, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	// OTP Correct, verify user
	update := bson.M{
		"$set":   bson.M{"status": "verified"},
		"$unset": bson.M{"otp": ""},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update user status", http.StatusInternalServerError)
		return
	}

	user.Status = "verified"

	utils.AddToLogMessage(&logMessageBuilder, "OTP verified successfully")

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
// ForgotPasswordHandler handles forgot password requests
func ForgotPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
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

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "User not found", http.StatusNotFound)
		return
	}

	// Generate OTP
	otpCode := ""
	for i := 0; i < 6; i++ {
		b := make([]byte, 1)
		rand.Read(b)
		otpCode += fmt.Sprintf("%d", int(b[0])%10)
	}

	// Update User with OTP
	update := bson.M{
		"$set": bson.M{"otp": otpCode},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to update user", http.StatusInternalServerError)
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
		"message": "OTP sent to your email.",
	})
}

// ResetPasswordHandler handles password reset with OTP
// ResetPasswordHandler handles password reset with OTP
func ResetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
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

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "User not found", http.StatusNotFound)
		return
	}

	if user.OTP != req.OTP {
		utils.RespondError(w, &logMessageBuilder, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	// Hash new password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	// Update password and clear OTP
	update := bson.M{
		"$set":   bson.M{"password": string(hashedPassword)},
		"$unset": bson.M{"otp": ""},
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
// ChangePasswordHandler handles password change for logged-in users
func ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
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

	collection := utils.GetCollection("fitly", "users")
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
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Delete Account API]")

	if r.Method != http.MethodDelete {
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

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Soft delete: update status to 'deleted' and set deleted_at
	update := bson.M{
		"$set": bson.M{
			"status":     "deleted",
			"deleted_at": time.Now(),
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

	utils.AddToLogMessage(&logMessageBuilder, "Account deleted successfully")
	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Account deleted successfully. You have been logged out.",
	})
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
}

// GoogleLoginHandler handles Google OAuth login
func GoogleLoginHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
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
		idTokenResp, idErr := http.Get("https://oauth2.googleapis.com/tokeninfo?id_token=" + req.GoogleToken)
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

	// Make sure name is populated
	name := googleUser.Name
	if name == "" {
		name = strings.Split(googleUser.Email, "@")[0]
	}

	collection := utils.GetCollection("fitly", "users")
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
		} else {
			utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		// Existing user
		if user.Status == "deleted" {
			utils.RespondError(w, &logMessageBuilder, "Account deleted. Please sign up again to create a new account.", http.StatusForbidden)
			return
		}
		// If they were pending, Google login verifies them
		if user.Status == "pending" {
			_, err := collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{"status": "active"}})
			if err != nil {
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
