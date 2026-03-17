package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type contextKey string

const UserIDKey contextKey = "user_id"

// AuthMiddleware validates JWT token and injects user_id into context
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		tokenString := parts[1]
		token, err := utils.ValidateToken(tokenString)
		if err != nil || !token.Valid {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "Invalid token claims", http.StatusUnauthorized)
			return
		}

		userID, ok := claims["user_id"].(string)
		if !ok {
			http.Error(w, "Invalid user ID in token", http.StatusUnauthorized)
			return
		}

		// Inject user_id into context
		ctx := context.WithValue(r.Context(), UserIDKey, userID)

		// Check user status in DB
		collection := utils.GetCollection("fitly", "users")
		ctxDb, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var user struct{ Status string }
		objID, _ := primitive.ObjectIDFromHex(userID)
		err = collection.FindOne(ctxDb, bson.M{"_id": objID}).Decode(&user)
		if err != nil || user.Status == "deleted" {
			http.Error(w, "Unauthorized: Account deleted or not found", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetUserIDFromContext helper to retrieve user_id from context
func GetUserIDFromContext(ctx context.Context) (string, error) {
	userID, ok := ctx.Value(UserIDKey).(string)
	if !ok {
		return "", fmt.Errorf("user_id not found in context")
	}
	return userID, nil
}

// ImageCacheMiddleware adds Cache-Control headers to responses for image-serving endpoints.
// immutable=true uses a 30-day max-age with immutable; immutable=false uses a 1-day max-age.
func ImageCacheMiddleware(next http.Handler, immutable bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if immutable {
			w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		next.ServeHTTP(w, r)
	})
}
