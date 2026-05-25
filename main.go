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
	http.Handle("/auth/guest", corsMiddleware(http.HandlerFunc(api.GuestTokenHandler)))
	http.Handle("/auth/forgot-password", corsMiddleware(http.HandlerFunc(api.ForgotPasswordHandler)))
	http.Handle("/auth/reset-password", corsMiddleware(http.HandlerFunc(api.ResetPasswordHandler)))
	http.Handle("/auth/change-password", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.ChangePasswordHandler))))
	http.Handle("/auth/delete-account", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.DeleteAccountHandler))))

	// Billing / quota
	http.Handle("/billing/status", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.BillingStatusHandler))))

	// Legal Routes
	http.Handle("/legal/privacy-policy", corsMiddleware(http.HandlerFunc(api.GetPrivacyPolicy)))
	http.Handle("/legal/terms-of-service", corsMiddleware(http.HandlerFunc(api.GetTermsOfService)))

	http.Handle("/product/details", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.ScrapeHandler)), true)))
	http.Handle("/product/upload", corsMiddleware(api.ImageCacheMiddleware(api.AuthMiddleware(http.HandlerFunc(api.UploadProductHandler)), true)))

	http.Handle("/themes", corsMiddleware(api.ImageCacheMiddleware(http.HandlerFunc(api.GetThemesHandler), true)))

	// Protected Routes (Require Token)
	// Person endpoints return user-specific JSON (not raw images), so we don't
	// wrap them in ImageCacheMiddleware. That middleware set
	// `Cache-Control: public, max-age=86400`, which (a) caused the client to
	// serve a stale list for 24h after a delete, masking the soft-delete in
	// the DB, and (b) allowed shared caches to store user-specific data.
	// The handler uses ETag-based revalidation via RespondJSONWithETag instead.
	http.Handle("/persons", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler))))
	http.Handle("/persons/", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.PersonHandler))))

	// Try-on endpoints: AuthMiddleware → QuotaMiddleware → handler.
	// QuotaMiddleware checks the per-user daily cap before invoking the handler
	// and bumps the counter after a successful 2xx response.
	http.Handle("/try-on", corsMiddleware(api.AuthMiddleware(api.QuotaMiddleware(http.HandlerFunc(api.VirtualTryOnHandler)))))
	http.Handle("/try-on/individual", corsMiddleware(api.AuthMiddleware(api.QuotaMiddleware(http.HandlerFunc(api.IndividualTryOnHandler)))))
	http.Handle("/try-on/couple", corsMiddleware(api.AuthMiddleware(api.QuotaMiddleware(http.HandlerFunc(api.CoupleTryOnHandler)))))
	http.Handle("/try-on/group", corsMiddleware(api.AuthMiddleware(api.QuotaMiddleware(http.HandlerFunc(api.GroupTryOnHandler)))))
	// Guest try-on: one-shot endpoint for anonymous users (no persistence).
	// Uses the same AuthMiddleware → QuotaMiddleware sandwich because guest
	// tokens come through the same path with plan=guest, capped at 1/day.
	http.Handle("/try-on/guest", corsMiddleware(api.AuthMiddleware(api.QuotaMiddleware(http.HandlerFunc(api.GuestTryOnHandler)))))
	// Gallery, wardrobe, and feedback all return user-specific JSON (not raw
	// images), and the response also contains presigned S3 URLs that expire
	// quickly. Wrapping these in ImageCacheMiddleware caused two bugs:
	//   (a) Cache-Control: public, max-age=2592000, immutable made the device
	//       serve the list from cache for 30 days, so new items didn't
	//       appear and deleted items reappeared on next visit.
	//   (b) After ~1h the presigned URLs inside the cached response would
	//       start 403'ing because the cache outlived the signatures.
	// Same reasoning as /persons (above): use ETag-based revalidation via
	// RespondJSONWithETag in the handlers instead.
	http.Handle("/gallery", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GalleryHandler))))
	http.Handle("/gallery/", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.GalleryHandler))))
	http.Handle("/feedback", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.FeedbackHandler))))
	http.Handle("/wardrobe", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.WardrobeHandler))))
	http.Handle("/wardrobe/", corsMiddleware(api.AuthMiddleware(http.HandlerFunc(api.WardrobeHandler))))

	port := config.Port
	fmt.Printf("Server starting on port %s...\n", port)
	if err := http.ListenAndServe(":"+port, http.DefaultServeMux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
