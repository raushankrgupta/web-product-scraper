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

	// Initialize S3
	if err := utils.InitS3(); err != nil {
		log.Fatalf("Failed to initialize S3: %v", err)
	}

	// CORS Middleware
	corsMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	// Serve Static Files
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	// Public Routes
	http.Handle("/auth/signup", corsMiddleware(http.HandlerFunc(api.SignupHandler)))
	http.Handle("/auth/verify-otp", corsMiddleware(http.HandlerFunc(api.VerifyOTPHandler)))
	http.Handle("/auth/login", corsMiddleware(http.HandlerFunc(api.LoginHandler)))
	http.Handle("/auth/forgot-password", corsMiddleware(http.HandlerFunc(api.ForgotPasswordHandler)))
	http.Handle("/auth/reset-password", corsMiddleware(http.HandlerFunc(api.ResetPasswordHandler)))
	http.Handle("/auth/change-password", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.ChangePasswordHandler))))

	http.Handle("/product/details", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.ScrapeHandler))))

	// Protected Routes (Require Token)
	http.Handle("/persons", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler))))
	http.Handle("/persons/", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler))))

	http.Handle("/try-on", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.VirtualTryOnHandler))))
	http.Handle("/gallery", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GalleryHandler))))
	http.Handle("/gallery/", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GalleryHandler))))

	port := config.Port
	fmt.Printf("Server starting on port %s...\n", port)
	if err := http.ListenAndServe(":"+port, utils.LatencyMiddleware(http.DefaultServeMux)); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
