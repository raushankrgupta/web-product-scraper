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
	corsMiddleware := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next(w, r)
		}
	}

	http.HandleFunc("/scrape", corsMiddleware(api.ScrapeHandler))
	http.HandleFunc("/create-profile", corsMiddleware(api.CreateProfileHandler))
	http.HandleFunc("/auth/google/login", corsMiddleware(api.GoogleLoginHandler))
	http.HandleFunc("/auth/google/callback", corsMiddleware(api.GoogleCallbackHandler))

	// Generic Auth Routes
	http.HandleFunc("/auth/signup", corsMiddleware(api.SignupHandler))
	http.HandleFunc("/auth/verify-email", corsMiddleware(api.VerifyEmailHandler))
	http.HandleFunc("/auth/verify-otp", corsMiddleware(api.VerifyOTPHandler))
	http.HandleFunc("/auth/login", corsMiddleware(api.LoginHandler))
	http.HandleFunc("/auth/forgot-password", corsMiddleware(api.ForgotPasswordHandler))
	http.HandleFunc("/auth/reset-password", corsMiddleware(api.ResetPasswordHandler))
	http.HandleFunc("/try-on", corsMiddleware(api.VirtualTryOnHandler))

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
