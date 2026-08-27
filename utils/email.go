package utils

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/resend/resend-go/v4"
)

// defaultEmailFrom is used when EMAIL_FROM is not set. The address' domain
// must be verified in the Resend dashboard, otherwise every send is rejected.
// For local testing before domain verification, set
// EMAIL_FROM="onboarding@resend.dev" — Resend allows that sender, but only to
// the email address the Resend account itself is registered with.
const defaultEmailFrom = "TryOnFusion App <no-reply@tryonfusion.com>"

// emailSendTimeout bounds a single Resend API call so an OTP request can't
// hang on the transactional email provider.
const emailSendTimeout = 15 * time.Second

var (
	resendOnce   sync.Once
	resendClient *resend.Client
	resendErr    error
)

// emailClient builds the Resend client once and reuses it. The API key is read
// at first use rather than at init so config.LoadConfig() (which loads .env)
// has already run.
func emailClient() (*resend.Client, error) {
	resendOnce.Do(func() {
		apiKey := strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
		if apiKey == "" {
			resendErr = fmt.Errorf("RESEND_API_KEY is not set in environment variables")
			return
		}
		resendClient = resend.NewClient(apiKey)
	})
	return resendClient, resendErr
}

// emailFrom returns the configured sender, accepting either a bare address
// ("no-reply@example.com") or an RFC 5322 pair ("App Name <no-reply@example.com>").
func emailFrom() string {
	if v := strings.TrimSpace(os.Getenv("EMAIL_FROM")); v != "" {
		return v
	}
	return defaultEmailFrom
}

// formatRecipient renders "Name <addr>" with the display name properly quoted,
// falling back to the bare address when no name is supplied.
func formatRecipient(name, addr string) string {
	name = strings.TrimSpace(name)
	addr = strings.TrimSpace(addr)
	if name == "" {
		return addr
	}
	return (&mail.Address{Name: name, Address: addr}).String()
}

// SendEmail sends a transactional email (OTP, password reset, verification)
// through Resend. It applies its own timeout; use SendEmailWithContext when the
// caller already has a request context to honour.
func SendEmail(toName, toEmail, subject, textContent, htmlContent string) error {
	ctx, cancel := context.WithTimeout(context.Background(), emailSendTimeout)
	defer cancel()
	return SendEmailWithContext(ctx, toName, toEmail, subject, textContent, htmlContent)
}

// SendEmailWithContext is SendEmail bound to the caller's context.
func SendEmailWithContext(ctx context.Context, toName, toEmail, subject, textContent, htmlContent string) error {
	client, err := emailClient()
	if err != nil {
		return err
	}

	if strings.TrimSpace(toEmail) == "" {
		return fmt.Errorf("cannot send email: recipient address is empty")
	}

	params := &resend.SendEmailRequest{
		From:    emailFrom(),
		To:      []string{formatRecipient(toName, toEmail)},
		Subject: subject,
		Text:    textContent,
		Html:    htmlContent,
	}

	sent, err := client.Emails.SendWithContext(ctx, params)
	if err != nil {
		// Rate limiting is worth calling out separately: it is transient and
		// the caller may want to retry rather than treat it as a hard failure.
		if errors.Is(err, resend.ErrRateLimit) {
			log.Printf("Resend rate limit hit sending to %s: %v", toEmail, err)
		} else {
			log.Printf("Error sending email to %s: %v", toEmail, err)
		}
		return err
	}

	log.Printf("Email sent successfully to %s. Resend id: %s", toEmail, sent.Id)
	return nil
}
