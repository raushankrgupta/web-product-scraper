package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
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

	// Generate Verification Token
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to generate token: %v", err))
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	verificationToken := hex.EncodeToString(tokenBytes)

	newUser := models.User{
		Name:              req.Name,
		Email:             req.Email,
		Password:          string(hashedPassword),
		DOB:               req.DOB,
		Gender:            req.Gender,
		Status:            "pending",
		VerificationToken: verificationToken,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	res, err := collection.InsertOne(ctx, newUser)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to create user: %v", err))
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	// Mock Sending Email
	verificationLink := fmt.Sprintf("http://localhost:8081/auth/verify-email?token=%s", verificationToken)
	mockEmailMsg := fmt.Sprintf("\n[EMAIL MOCK] To: %s\nSubject: Verify your email\nBody: Click here to verify: %s\n\n", req.Email, verificationLink)
	fmt.Printf(mockEmailMsg)
	utils.AddToLogMessage(&logMessageBuilder, "User registered successfully. Sent mock verification email.")

	newUser.ID = res.InsertedID.(interface{}).(primitive.ObjectID) // Cast for JSON response if needed, but we don't return password

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "User registered successfully. Please accept the verification link sent to your email.",
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

	utils.AddToLogMessage(&logMessageBuilder, "Login successful")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Login successful",
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

	// Check if user exists (optional, depends on security policy to reveal existence)
	// For friendly UX, we usually check.
	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": req.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// Don't reveal user doesn't exist, just say email sent if we want to be secure.
			// But for this task I will validation
			utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("User not found: %s", req.Email))
			http.Error(w, "User with this email not found", http.StatusNotFound)
			return
		}
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Database error: %v", err))
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Mock sending email
	// In a real app: utils.SendEmail(req.Email, "Reset Password", "Link...")

	// Just log it for now
	config.LoadConfig() // Ensure config is loaded if we need email settings
	// fmt.Printf("Mock: Sending recovery email to %s\n", req.Email)
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Mock sending recovery email to %s", req.Email))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "If the email is registered, a password recovery link has been sent.",
	})
}
