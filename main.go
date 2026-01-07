package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/raushankrgupta/web-product-scraper/api"
	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

func main() {
	config.LoadConfig()

	// Initialize MongoDB
	if err := utils.ConnectMongo(config.MongoURI); err != nil {
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}

	// CORS Middleware
	corsMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	// Public Routes
	http.Handle("/auth/signup", corsMiddleware(http.HandlerFunc(api.SignupHandler)))
	http.Handle("/auth/verify-email", corsMiddleware(http.HandlerFunc(api.VerifyEmailHandler)))
	http.Handle("/auth/verify-otp", corsMiddleware(http.HandlerFunc(api.VerifyOTPHandler)))
	http.Handle("/auth/login", corsMiddleware(http.HandlerFunc(api.LoginHandler)))
	http.Handle("/auth/forgot-password", corsMiddleware(http.HandlerFunc(api.ForgotPasswordHandler)))
	http.Handle("/auth/reset-password", corsMiddleware(http.HandlerFunc(api.ResetPasswordHandler)))
	http.Handle("/auth/google/login", corsMiddleware(http.HandlerFunc(api.GoogleLoginHandler)))
	http.Handle("/auth/google/callback", corsMiddleware(http.HandlerFunc(api.GoogleCallbackHandler)))

	http.Handle("/product/details", corsMiddleware(http.HandlerFunc(api.ScrapeHandler))) // Protected or Public? Requirements said "strict security suggests protecting"
	// Let's protect sensitive ones.

	// Protected Routes (Require Token)
	http.Handle("/persons", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler))))
	http.Handle("/persons/", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler))))

	http.Handle("/fitting/generate", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GenerateFittingHandler))))

	// Deprecated /create-profile (redirect or keep for backward compat? I'll remove it as per plan to supersede it)
	// http.HandleFunc("/create-profile", corsMiddleware(api.CreateProfileHandler))
	// http.HandleFunc("/scrape", corsMiddleware(api.ScrapeHandler)) // Superseded by /product/details

	http.Handle("/try-on", corsMiddleware(http.HandlerFunc(api.VirtualTryOnHandler))) // Protect this too?
	// http.Handle("/try-on", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.VirtualTryOnHandler))))

	// Serve static files for images
	http.Handle("/product_images/", http.StripPrefix("/product_images/", http.FileServer(http.Dir("product_images"))))
	http.Handle("/user_images/", http.StripPrefix("/user_images/", http.FileServer(http.Dir("user_images"))))

	port := config.Port
	fmt.Printf("Server starting on port %s...\n", port)
	fmt.Printf("Usage: curl \"http://localhost:%s/scrape?url=<product_url>\"\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
