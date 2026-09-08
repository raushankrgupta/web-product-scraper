package utils

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// sessionTTL is how long a signed-in session lasts.
//
// Thirty days, matching the guest token, because there is no refresh flow: the
// app's 401 interceptor purges the session and routes to login, so the token
// lifetime *is* the logout interval. At the previous 24 hours that meant every
// user was signed out once a day, which production showed for what it is —
// two 401s on /billing/status, a password-reset OTP three minutes later, and a
// fresh login. People do not remember a password they are asked for daily.
const sessionTTL = 30 * 24 * time.Hour

// GenerateToken generates a JWT token for the user
func GenerateToken(userID string) (string, error) {
	jwtSecret := []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		return "", fmt.Errorf("JWT_SECRET is not set")
	}

	claims := jwt.MapClaims{
		"user_id": userID,
		"exp":     time.Now().Add(sessionTTL).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// GenerateGuestToken issues a JWT for an anonymous "guest" session bound to a
// device. The token carries `guest: true` in the claims and a longer 30-day
// expiry so a returning user keeps the same session across app launches —
// the per-day quota (not the token lifetime) is the actual rate limit.
func GenerateGuestToken(userID string) (string, error) {
	jwtSecret := []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		return "", fmt.Errorf("JWT_SECRET is not set")
	}

	claims := jwt.MapClaims{
		"user_id": userID,
		"guest":   true,
		"exp":     time.Now().Add(sessionTTL).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// ValidateToken parses and validates the token
func ValidateToken(tokenString string) (*jwt.Token, error) {
	jwtSecret := []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		return nil, fmt.Errorf("JWT_SECRET is not configured on the server")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	if token == nil || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return token, nil
}
