package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

// GetPrivacyPolicy returns the dynamic privacy policy content
func GetPrivacyPolicy(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Get Privacy Policy API]")

	policy := fmt.Sprintf(`
# Privacy Policy

**Effective Date:** 2026-01-20

## 1. Introduction
Welcome to TryOnFusion ("we," "our," or "us"). This Privacy Policy explains how we collect, use, disclose, and safeguard your information when you access our mobile application (the "App") and our website (the "Site").

## 2. Information We Collect
We collect information that identifies, relates to, describes, references, is capable of being associated with, or could reasonably be linked, directly or indirectly, with you ("Personal Information").

### A. Personal Data
While using our Service, we may ask you to provide us with certain personally identifiable information that can be used to contact or identify you, including:
- Name
- Email address
- Phone number
- Gender, Age, Height, Weight (for fitting purposes)

### B. Media & Photos (Sensitive Data)
Our App collects user photos to enable the "Virtual Try-On" feature.
- **What we do:** We upload your full-body photo to our secure servers to process the virtual try-on using AI.
- **Storage:** These images are stored securely on AWS S3.
- **Deletion:** You can delete your photos at any time via the specific deletion option in the Gallery or by deleting your account.

## 3. How We Use Your Information
We use the information we collect to:
- Provide the functionality of the App (specifically, Virtual Try-On).
- Manage your account and provide customer support.
- Communicate with you about updates or offers (if you opted in).

## 4. Account Deletion
You have the right to delete your account and all associated data.
- **In-App:** Go to Profile -> Settings -> Delete Account.
- **Web:** Please contact us at %s to request deletion if you cannot access the app.
Upon deletion, all your personal data and uploaded images are permanently removed from our active databases.

## 5. Contact Us
If you have questions or comments about this policy, you may contact us at:
- Email: %s
- Phone: %s
`, config.ContactEmail, config.ContactEmail, config.ContactPhone)

	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"content": policy,
	})
}

// GetTermsOfService returns the dynamic terms of service content
func GetTermsOfService(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		fmt.Println(logMessageBuilder.String())
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Get Terms of Service API]")

	terms := fmt.Sprintf(`
# Terms of Service

**Last Updated:** 2026-01-20

## 1. Agreement to Terms
By accessing or using TryOnFusion, you agree to be bound by these Terms. If you disagree with any part of the terms, then you may not access the Service.

## 2. User Accounts
When you create an account with us, you must provide information that is accurate, complete, and current at all times. Failure to do so constitutes a breach of the Terms, which may result in immediate termination of your account.

## 3. Content
Our Service allows you to upload, link, store, share and otherwise make available certain information, text, graphics, videos, or other material ("Content"). You are responsible for the Content that you post to the Service, including its legality, reliability, and appropriateness.

## 4. Virtual Try-On Disclaimer
The Virtual Try-On feature uses AI estimation. Results may vary and are for reference only. We do not guarantee 100%% accuracy in sizing or visual representation.

## 5. Termination
We may terminate or suspend your account immediately, without prior notice or liability, for any reason whatsoever, including without limitation if you breach the Terms.
Upon termination, your right to use the Service will immediately cease.

## 6. Contact Us
If you have any questions about these Terms, please contact us:
- Email: %s
`, config.ContactEmail)

	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"content": terms,
	})
}
