package utils

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestValidateToken_EmptySecretBlocked(t *testing.T) {
	orig := os.Getenv("JWT_SECRET")
	defer os.Setenv("JWT_SECRET", orig)

	// Set empty JWT secret
	os.Setenv("JWT_SECRET", "")

	// A token signed with empty secret ""
	claims := jwt.MapClaims{
		"user_id": "attacker_spoofed_user",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	forgedToken, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(""))

	// ValidateToken MUST fail
	_, err := ValidateToken(forgedToken)
	if err == nil {
		t.Fatalf("expected error when validating with empty JWT_SECRET, got nil")
	}
}

func TestToken_ValidFlow(t *testing.T) {
	orig := os.Getenv("JWT_SECRET")
	defer os.Setenv("JWT_SECRET", orig)

	secret := "a_very_secure_test_secret_with_more_than_32_characters!"
	os.Setenv("JWT_SECRET", secret)

	tok, err := GenerateToken("user_12345")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	parsed, err := ValidateToken(tok)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("expected jwt.MapClaims, got %T", parsed.Claims)
	}
	if claims["user_id"] != "user_12345" {
		t.Errorf("expected user_id user_12345, got %v", claims["user_id"])
	}
}

func TestGuestToken_ValidFlow(t *testing.T) {
	orig := os.Getenv("JWT_SECRET")
	defer os.Setenv("JWT_SECRET", orig)

	secret := "a_very_secure_test_secret_with_more_than_32_characters!"
	os.Setenv("JWT_SECRET", secret)

	tok, err := GenerateGuestToken("guest:device_abc")
	if err != nil {
		t.Fatalf("GenerateGuestToken failed: %v", err)
	}

	parsed, err := ValidateToken(tok)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("expected jwt.MapClaims, got %T", parsed.Claims)
	}
	if claims["user_id"] != "guest:device_abc" {
		t.Errorf("expected guest:device_abc, got %v", claims["user_id"])
	}
	if claims["guest"] != true {
		t.Errorf("expected guest: true, got %v", claims["guest"])
	}
}
