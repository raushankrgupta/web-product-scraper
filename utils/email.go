package utils

import (
	"fmt"
	"log"
	"os"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

// SendEmail sends an email using SendGrid
func SendEmail(toName, toEmail, subject, textContent, htmlContent string) error {
	apiKey := os.Getenv("SENDGRID_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("SENDGRID_API_KEY is not set in environment variables")
	}

	from := mail.NewEmail("Fitly App", "no-reply@tryonfusion.com") // Replace with a verified sender if you have one, or use a dummy for now check logs
	to := mail.NewEmail(toName, toEmail)
	message := mail.NewSingleEmail(from, subject, to, textContent, htmlContent)
	client := sendgrid.NewSendClient(apiKey)

	response, err := client.Send(message)
	if err != nil {
		log.Printf("Error sending email to %s: %v", toEmail, err)
		return err
	}

	if response.StatusCode >= 400 {
		log.Printf("SendGrid API Error: Status Code %d, Body: %s", response.StatusCode, response.Body)
		return fmt.Errorf("failed to send email, status code: %d", response.StatusCode)
	}

	log.Printf("Email sent successfully to %s. Status Code: %d", toEmail, response.StatusCode)
	return nil
}
