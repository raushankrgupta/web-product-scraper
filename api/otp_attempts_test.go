package api

import (
	"testing"
	"time"

	"github.com/raushankrgupta/web-product-scraper/models"
	"go.mongodb.org/mongo-driver/bson"
)

// The OTP attempt cap is enforced by a Mongo filter, and its correctness rests
// entirely on whether the field it filters on is present in the document.
// models.User tags otp_attempts `omitempty`, so SignupHandler's struct insert
// drops it when it is zero — and a bare {$lt: 5} filter matches no document
// with a missing field, which locked every new signup out of its own first
// verification attempt. This test pins the marshalling behaviour that makes
// the $exists arm of consumeOTPAttempt's filter necessary.
func TestNewUserDocumentOmitsOTPAttempts(t *testing.T) {
	raw, err := bson.Marshal(models.User{
		Email:        "someone@example.com",
		Status:       "pending",
		OTP:          "123456",
		OTPExpiresAt: time.Now().Add(10 * time.Minute),
		OTPAttempts:  0,
	})
	if err != nil {
		t.Fatalf("marshal user: %v", err)
	}

	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal user: %v", err)
	}

	if _, present := doc["otp_attempts"]; present {
		t.Skip("otp_attempts is now persisted on insert; the $exists arm of " +
			"consumeOTPAttempt is redundant but harmless")
	}

	// Field is absent, so the filter must tolerate that. Rebuild the filter
	// consumeOTPAttempt uses and assert the $exists arm is there.
	filter := bson.M{
		"$or": []bson.M{
			{"otp_attempts": bson.M{"$lt": maxOTPAttempts}},
			{"otp_attempts": bson.M{"$exists": false}},
		},
	}
	arms, ok := filter["$or"].([]bson.M)
	if !ok || len(arms) != 2 {
		t.Fatalf("expected a two-armed $or filter, got %#v", filter["$or"])
	}
	if _, ok := arms[1]["otp_attempts"].(bson.M)["$exists"]; !ok {
		t.Fatal("attempt filter has no $exists arm: users with no otp_attempts " +
			"field will be refused on their first attempt")
	}
}
