package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

var (
	MongoURI           string
	Port               string
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	GeminiAPIKey       string
)

// LoadConfig loads environment variables from .env file
func LoadConfig() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using default values or system environment variables")
	}

	MongoURI = os.Getenv("MONGO_URI")
	if MongoURI == "" {
		MongoURI = "mongodb://localhost:27017/"
	}

	Port = os.Getenv("PORT")
	if Port == "" {
		Port = "8080"
	}

	GoogleClientID = os.Getenv("GOOGLE_CLIENT_ID")
	GoogleClientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
	GoogleRedirectURL = os.Getenv("GOOGLE_REDIRECT_URL")
	if GoogleRedirectURL == "" {
		GoogleRedirectURL = "http://localhost:8080/auth/google/callback"
	}

	GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
}
