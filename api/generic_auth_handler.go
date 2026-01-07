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
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Signup API]")

	if r.Method != http.MethodPost {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Basic Validation
	if req.Name == "" || req.Email == "" || req.Password == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Missing required fields (Name, Email, Password)")
		http.Error(w, "Name, Email and Password are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if user already exists
	var existingUser models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&existingUser)
	if err == nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("User with email %s already exists", req.Email))
		http.Error(w, "User with this email already exists", http.StatusConflict)
		return
	} else if err != mongo.ErrNoDocuments {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Database error checking user: %v", err))
		http.Error(w, "Database error checking user", http.StatusInternalServerError)
		return
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to hash password: %v", err))
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	// Temporary: Generate OTP manually here since we don't have utils.GenerateOTP yet
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
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to create user: %v", err))
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
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

	newUser.ID = res.InsertedID.(interface{}).(primitive.ObjectID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "User registered successfully. Please verify your email using the OTP sent.",
		"user":    newUser,
	})
}

// LoginHandler handles user login
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Login API]")

	if r.Method != http.MethodPost {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Email == "" || req.Password == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Email and Password are required")
		http.Error(w, "Email and Password are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("User not found: %s", req.Email))
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
		} else {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Database error: %v", err))
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	// Compare password
	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password))
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, "Invalid password")
		http.Error(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}

	// Check status
	if user.Status == "pending" {
		utils.AddToLogMessage(&logMessageBuilder, "User email pending verification")
		http.Error(w, "Please verify your email first", http.StatusForbidden)
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
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to generate token: %v", err))
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Login successful")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Login successful",
		"token":   token,
		"user":    user,
	})
}

// VerifyEmailHandler handles email verification logic
func VerifyEmailHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Verify Email API]")

	if r.Method != http.MethodGet {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Token is required")
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"verification_token": token}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.AddToLogMessage(&logMessageBuilder, "Invalid or expired verification token")
			http.Error(w, "Invalid or expired verification token", http.StatusBadRequest)
		} else {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Database error: %v", err))
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	// Update user status and clear token
	update := bson.M{
		"$set":   bson.M{"status": "verified"},
		"$unset": bson.M{"verification_token": ""},
	}

	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to verify user: %v", err))
		http.Error(w, "Failed to verify user", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Email verification completed")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Email verification completed! Kindly proceed with login",
	})
}

// VerifyOTPHandler handles OTP verification
func VerifyOTPHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Verify OTP API]")

	if r.Method != http.MethodPost {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req VerifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Email == "" || req.OTP == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Email and OTP are required")
		http.Error(w, "Email and OTP are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("User not found: %s", req.Email))
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Database error: %v", err))
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	if user.Status == "verified" || user.Status == "active" {
		if user.OTP != req.OTP {
			utils.AddToLogMessage(&logMessageBuilder, "Invalid OTP")
			http.Error(w, "Invalid OTP", http.StatusUnauthorized)
			return
		}
		// If verified/active and OTP matches, we assume it's for Password Reset flow.
		// We return success but DO NOT clear OTP yet, as it's needed for ResetPassword API.
		// A more secure way would be to return a temporary reset token, but per requirements we use OTP.
		utils.AddToLogMessage(&logMessageBuilder, "OTP verified for password reset")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"message": "OTP verified successfully. Please proceed to reset password.",
		})
		return
	}

	if user.OTP != req.OTP {
		utils.AddToLogMessage(&logMessageBuilder, "Invalid OTP")
		http.Error(w, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	// OTP Correct, verify user
	update := bson.M{
		"$set":   bson.M{"status": "verified"},
		"$unset": bson.M{"otp": ""},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to update user status: %v", err))
		http.Error(w, "Failed to update user status", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "OTP verified successfully")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Email verification successful! You can now login.",
	})
}

// ForgotPasswordHandler handles forgot password requests
func ForgotPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Forgot Password API]")

	if r.Method != http.MethodPost {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Email == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Email is required")
		http.Error(w, "Email is required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("User not found: %s", req.Email))
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// Generate OTP
	// Temporary: Generate OTP manually here since we don't have utils.GenerateOTP yet
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
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to update user OTP: %v", err))
		http.Error(w, "Failed to update user", http.StatusInternalServerError)
		return
	}

	// Send OTP Email
	emailErr := utils.SendEmail(user.Name, req.Email, "Reset Password OTP",
		fmt.Sprintf("Your OTP for password reset is: %s", otpCode),
		fmt.Sprintf("<h1>Your OTP for password reset is: <strong>%s</strong></h1>", otpCode))

	if emailErr != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to send email: %v", emailErr))
		http.Error(w, "Failed to send email", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "OTP for password reset sent")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "OTP sent to your email.",
	})
}

// ResetPasswordHandler handles password reset with OTP
func ResetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Reset Password API]")

	if r.Method != http.MethodPost {
		utils.AddToLogMessage(&logMessageBuilder, "Method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Email == "" || req.OTP == "" || req.NewPassword == "" {
		utils.AddToLogMessage(&logMessageBuilder, "Email, OTP and New Password are required")
		http.Error(w, "Email, OTP and New Password are required", http.StatusBadRequest)
		return
	}

	collection := utils.GetCollection("fitly", "users")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("User not found: %s", req.Email))
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	if user.OTP != req.OTP {
		utils.AddToLogMessage(&logMessageBuilder, "Invalid OTP")
		http.Error(w, "Invalid OTP", http.StatusUnauthorized)
		return
	}

	// Hash new password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to hash password: %v", err))
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	// Update password and clear OTP
	update := bson.M{
		"$set":   bson.M{"password": string(hashedPassword)},
		"$unset": bson.M{"otp": ""},
	}
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to update password: %v", err))
		http.Error(w, "Failed to update password", http.StatusInternalServerError)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, "Password reset successfully")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Password reset successfully. Please login with your new password.",
	})
}
