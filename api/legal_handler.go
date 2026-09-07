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
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Get Privacy Policy API]")

	policy := fmt.Sprintf(`
# Privacy Policy

**Effective Date:** 2026-09-07

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

## 4. Link Import (in-app browser)
When you open a link with Link Import, the page is loaded on your device in a private window that does not share cookies or logins with your normal browser and is cleared when you close it. We do not receive your browsing activity, the page content, cookies, or anything you type on that website.

When you tap Import, we receive only the images you selected, the page address, and the page title, which are stored in your account so you can use them for try-ons. For service quality we record the website's domain name and the number of images imported.

## 5. Account Deletion
You have the right to delete your account and all associated data.
- **In-App:** Go to Profile -> Settings -> Delete Account.
- **Web:** Please contact us at %s to request deletion if you cannot access the app.
Upon deletion, all your personal data and uploaded images are permanently removed from our active databases.

## 6. Contact Us
If you have questions or comments about this policy, you may contact us at:
- Email: %s
- Email: %s
`, config.ContactEmail, config.ContactEmail, config.ContactEmail)

	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"content": policy,
	})
}

// GetTermsOfService returns the dynamic terms of service content.
//
// Sections 3–8 were added with Link Import. They are the "publish rules that
// prohibit infringing content, name a grievance officer, describe takedown"
// due diligence that the IT (Intermediary Guidelines) Rules, 2021 make a
// condition of the §79 safe harbour — which is what the company relies on for
// images users import from third-party sites and store here. Legacy clients
// render this same markdown, so the text reaches every install.
func GetTermsOfService(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Get Terms of Service API]")

	utils.RespondJSON(w, http.StatusOK, map[string]string{
		"content": termsOfServiceMarkdown(),
	})
}

// termsOfServiceMarkdown renders the Terms with the grievance officer details
// from config. Kept separate from the handler so tests can pin the sections.
func termsOfServiceMarkdown() string {
	officer := config.GrievanceOfficerName
	if officer == "" {
		officer = "Grievance Officer, TryOnFusion"
	}
	address := config.LegalPostalAddress
	if address == "" {
		address = "postal address available on request by email"
	}

	// %% escapes the literal percent sign for fmt.Sprintf.
	return fmt.Sprintf(`
# Terms of Service

**Last Updated:** 2026-09-07

## 1. Agreement to Terms
By accessing or using TryOnFusion (the "Service"), you agree to be bound by these Terms. If you disagree with any part of the terms, then you may not access the Service.

## 2. User Accounts
When you create an account with us, you must provide information that is accurate, complete, and current at all times. Failure to do so constitutes a breach of the Terms, which may result in immediate termination of your account.

## 3. Your Content
Our Service lets you upload photos and other material, including images you choose to import from third-party websites using the in-app browser ("Content"). You keep ownership of your Content. You grant us a limited, non-exclusive licence to store and process your Content only to provide the Service to you (for example, to generate a try-on image you requested).

You are responsible for your Content. You confirm that you have the right to upload it and to use it for a personal virtual try-on, that it does not infringe anyone's rights, and that it does not include photos of other people without their permission.

## 4. Link Import Tool
The Link Import tool opens a website you choose inside a private browser window on your own device. The website is loaded by your device, not by our servers, and you are subject to that website's terms while you browse it. You decide which images to import; only the images you select, the page address, and the page title are sent to us.

We do not endorse, and are not affiliated with, any website or brand you import from. You must use imported images only for your own personal, non-commercial virtual try-on. Do not use the tool to build collections of third-party images, to redistribute them, or in any way that a website has told you it does not permit.

## 5. Intellectual Property Complaints and Takedown
We respect intellectual property rights and act on valid complaints. If you believe Content on the Service infringes your rights, email %s with:

- (a) the Content or account concerned,
- (b) the work you say is infringed and proof of your rights,
- (c) your contact details, and
- (d) a statement that the information is accurate.

We will acknowledge within 24 hours and act within the timeframes required by law, including within 36 hours of a court or government order under the Information Technology Act, 2000. Accounts that repeatedly infringe will be terminated.

## 6. Grievance Officer
In accordance with the Information Technology Act, 2000 and the rules made under it, the Grievance Officer is:

- Name: %s
- Email: %s
- Address: %s

Complaints are acknowledged within 24 hours and resolved within 15 days.

## 7. Acceptable Use
You must not: upload or import content that is unlawful, infringing, or depicts a person without their consent; use the Service to create deceptive or sexual imagery of real people; attempt to access websites in a manner they prohibit; or interfere with the Service.

## 8. Indemnity and Liability
You agree to indemnify us against claims arising from your Content or your use of the Link Import tool in breach of these Terms. To the extent permitted by law, our liability is limited to the amount you paid us in the 12 months before the claim.

## 9. Virtual Try-On Disclaimer
The Virtual Try-On feature uses AI estimation. Results may vary and are for reference only. We do not guarantee 100%% accuracy in sizing or visual representation.

## 10. Termination
We may terminate or suspend your account immediately, without prior notice or liability, for any reason whatsoever, including without limitation if you breach the Terms.
Upon termination, your right to use the Service will immediately cease.

## 11. Contact Us
If you have any questions about these Terms, please contact us:
- Email: %s
`, config.GrievanceEmail, officer, config.GrievanceEmail, address, config.ContactEmail)
}
