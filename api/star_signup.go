package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/utils"
)

// grantSignupBonus issues the welcome credits for a newly usable account and
// records the email identity that governs how large that bonus is.
//
// The two halves are deliberately together. RecordSignup is what detects an
// address that has registered before — which is the entire defence against
// deleting an account to farm the welcome bonus — and GrantWelcomeCredits
// reads that answer to choose between the full grant and the smaller
// returning grant.
//
// Called from every path that turns a signup into a usable account: OTP
// verification for email/password, and first Google login. Both are safe to
// call more than once; the welcome_granted flag on the balance makes the
// grant itself idempotent.
func grantSignupBonus(userID, email string, logger *strings.Builder) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	returning, err := utils.RecordSignup(ctx, email)
	if err != nil {
		// RecordSignup already fails open; this branch is belt and braces.
		utils.AddToLogMessage(logger, fmt.Sprintf("signup identity check failed: %v", err))
	}

	grant, err := utils.GrantWelcomeCredits(ctx, userID, returning)
	if err != nil {
		// A failed grant must not fail the signup — the user is registered
		// either way, and stars can be added by hand. Losing the account
		// over a bonus would be the worse trade.
		utils.AddToLogMessage(logger, fmt.Sprintf("welcome grant failed: %v", err))
		return
	}

	switch {
	case !grant.Any():
		utils.AddToLogMessage(logger, "welcome gift already granted; none issued")
	case returning:
		utils.AddToLogMessage(logger, fmt.Sprintf(
			"granted returning-user welcome gift: %d stars, %d free credits (this email has registered before)",
			grant.Stars, grant.Credits))
	default:
		utils.AddToLogMessage(logger, fmt.Sprintf(
			"granted welcome gift: %d stars, %d free credits", grant.Stars, grant.Credits))
	}
}

// releaseSignupIdentity records a deletion and clears the account's star
// state.
//
// The balance is deleted because unspent stars must not survive into a
// re-registration; the identity record is kept, because it holds only a
// peppered hash and is the thing that makes the returning-user rule work at
// all. The ledger is kept too — it is the financial record behind real
// payments and is what a refund dispute is answered from.
func releaseSignupIdentity(userID, email string, logger *strings.Builder) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	utils.AddToLogMessage(logger, "star state at deletion: "+
		utils.StarStateSummaryForSupport(ctx, userID))

	utils.RecordAccountDeletion(ctx, email)
	utils.PurgeUserStarState(ctx, userID)

	// The referral code is per account and must not survive it: a code
	// printed on someone's screen should stop working when they leave, and
	// the next account would otherwise inherit its earned-star counters.
	// The redemption records stay — they are keyed on the email hash and are
	// what stops delete-and-rejoin from farming the referee bonus.
	utils.PurgeReferralCode(ctx, userID)
}
