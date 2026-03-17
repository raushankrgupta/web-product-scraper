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
		return utils.LatencyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}

	// Serve Static Files
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	// Public Routes
	http.Handle("/auth/signup", corsMiddleware(http.HandlerFunc(api.SignupHandler)))
	http.Handle("/auth/verify-otp", corsMiddleware(http.HandlerFunc(api.VerifyOTPHandler)))
	http.Handle("/auth/login", corsMiddleware(http.HandlerFunc(api.LoginHandler)))
	http.Handle("/auth/google", corsMiddleware(http.HandlerFunc(api.GoogleLoginHandler)))
	http.Handle("/auth/forgot-password", corsMiddleware(http.HandlerFunc(api.ForgotPasswordHandler)))
	http.Handle("/auth/reset-password", corsMiddleware(http.HandlerFunc(api.ResetPasswordHandler)))
	http.Handle("/auth/change-password", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.ChangePasswordHandler))))
	http.Handle("/auth/delete-account", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.DeleteAccountHandler))))

	// Legal Routes
	http.Handle("/legal/privacy-policy", corsMiddleware(http.HandlerFunc(api.GetPrivacyPolicy)))
	http.Handle("/legal/terms-of-service", corsMiddleware(http.HandlerFunc(api.GetTermsOfService)))

	http.Handle("/product/details", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.ScrapeHandler)), true)))
	http.Handle("/product/upload", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.UploadProductHandler)), true)))

	http.Handle("/themes", corsMiddleware(api.ImageCacheMiddleware(http.HandlerFunc(api.GetThemesHandler), true)))

	// Protected Routes (Require Token)
	// Person endpoints use mutable caching (profile photos can change)
	http.Handle("/persons", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler)), false)))
	http.Handle("/persons/", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler)), false)))

	http.Handle("/try-on", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.VirtualTryOnHandler))))
	http.Handle("/try-on/individual", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.IndividualTryOnHandler))))
	http.Handle("/try-on/couple", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.CoupleTryOnHandler))))
	http.Handle("/try-on/group", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GroupTryOnHandler))))
	http.Handle("/gallery", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GalleryHandler)), true)))
	http.Handle("/gallery/", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GalleryHandler)), true)))
	http.Handle("/feedback", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.FeedbackHandler))))
	http.Handle("/wardrobe", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.WardrobeHandler)), true)))
	http.Handle("/wardrobe/", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.WardrobeHandler)), true)))

	port := config.Port
	fmt.Printf("Server starting on port %s...\n", port)
	if err := http.ListenAndServe(":"+port, http.DefaultServeMux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
