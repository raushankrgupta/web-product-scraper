package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type contextKey string

const (
	UserIDKey   contextKey = "user_id"
	UserPlanKey contextKey = "user_plan"
	IsGuestKey  contextKey = "is_guest"
)

// CachedResultHeader marks a try-on response that was served from the
// in-process result cache rather than generated. StarGateMiddleware reads it
// and refunds the hold: a response that cost us no upstream call must not
// cost the user any stars.
const CachedResultHeader = "X-TryOn-Cached"

// AuthMiddleware validates JWT token and injects user_id, plan, and guest flag
// into the request context. Guest tokens (issued by /auth/guest) skip the
// users-collection check and always use plan = "guest".
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These used to be http.Error, i.e. text/plain — which the mobile
		// app's error normaliser can't read, so every auth failure surfaced
		// as an opaque string. The `reason` is logged (not returned) so the
		// two cases that look identical from outside — no header at all
		// versus an expired token — are distinguishable in the logs.
		reject := func(reason string) {
			utils.L(r.Context()).Warn("auth rejected",
				"reason", reason, "path", r.URL.Path,
				"has_auth_header", r.Header.Get("Authorization") != "")
			utils.RespondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Please sign in again to continue.",
			})
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			reject("missing Authorization header")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			reject("malformed Authorization header")
			return
		}

		tokenString := parts[1]
		token, err := utils.ValidateToken(tokenString)
		if err != nil || !token.Valid {
			reject("invalid or expired token")
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			reject("invalid token claims")
			return
		}

		userID, ok := claims["user_id"].(string)
		if !ok {
			reject("no user id in token")
			return
		}

		// Guest tokens carry user_id = "guest:<device_id>" and are signed with the
		// same secret. They never hit the users collection.
		isGuest := false
		if g, ok := claims["guest"].(bool); ok && g {
			isGuest = true
		}
		if strings.HasPrefix(userID, "guest:") {
			isGuest = true
		}

		ctx := context.WithValue(r.Context(), UserIDKey, userID)
		ctx = context.WithValue(ctx, IsGuestKey, isGuest)

		if isGuest {
			ctx = context.WithValue(ctx, UserPlanKey, models.PlanGuest)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Real user — look up status + plan in DB
		collection := utils.GetCollection(config.DBName, "users")
		ctxDb, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var user struct {
			Status string `bson:"status"`
			Plan   string `bson:"plan"`
		}
		objID, _ := primitive.ObjectIDFromHex(userID)
		err = collection.FindOne(ctxDb, bson.M{"_id": objID}).Decode(&user)
		if err != nil || user.Status == "deleted" {
			reject("account deleted or not found")
			return
		}

		plan := user.Plan
		if plan == "" {
			plan = models.PlanFree
		}
		ctx = context.WithValue(ctx, UserPlanKey, plan)

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

// GetUserPlanFromContext returns the plan stored by AuthMiddleware. Falls back
// to PlanFree if the value isn't present (defensive default — should never
// happen if the handler is properly wrapped).
func GetUserPlanFromContext(ctx context.Context) string {
	plan, ok := ctx.Value(UserPlanKey).(string)
	if !ok || plan == "" {
		return models.PlanFree
	}
	return plan
}

// IsGuestFromContext returns true if the request was authenticated via a guest
// token. Useful for branching behaviour (e.g. watermarking, upsell prompts).
func IsGuestFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(IsGuestKey).(bool)
	return v
}

// statusRecorder lets StarGateMiddleware see the response status — which is
// what decides whether the star hold is committed or refunded — and
// RequestLogMiddleware see the response size, without touching the body.
// Write() implicitly calls WriteHeader(200), which we capture via the default
// `status` field.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	written     int
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.written += n
	return n, err
}

// Flush forwards to the underlying writer when it supports flushing, so
// wrapping a handler in this recorder can't break a streaming response.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
