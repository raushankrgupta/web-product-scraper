# TryOnFusion Backend

Backend API server for the TryOnFusion virtual try-on platform. Built with Go, this service handles authentication, product scraping from e-commerce sites, AI-powered virtual try-on image generation, and wardrobe management.

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.24 |
| Web Server | `net/http` (stdlib) |
| Database | MongoDB |
| Object Storage | AWS S3 |
| AI Image Generation | Google Gemini |
| Authentication | JWT + Google OAuth |
| Email Service | SendGrid |
| Web Scraping | goquery, chromedp, Selenium |
| Reverse Proxy | Caddy |
| Containerization | Docker, Docker Compose |

## Features

- **User Authentication** -- Email/password signup with OTP verification, Google OAuth, password reset
- **Product Scraping** -- Extracts product details (title, price, images, variants) from Amazon, Flipkart, Myntra, TataCliq, and Peter England
- **Virtual Try-On** -- AI-powered outfit visualization using Google Gemini for individual, couple, and group modes
- **Wardrobe Management** -- Save, categorize, and manage clothing items
- **Gallery** -- Browse, favorite, save, and provide feedback on generated try-on images
- **Themed Try-Ons** -- Pre-built themes for creative outfit compositions
- **Person Profiles** -- Manage body measurements and photos for accurate try-on results

## Project Structure

```
web-product-scraper/
├── main.go                  # Application entry point, route registration
├── api/                     # HTTP handlers
│   ├── handler.go           # Product scraping handler
│   ├── middleware.go         # Auth middleware
│   ├── generic_auth_handler.go  # Authentication endpoints
│   ├── profile_handler.go   # Person CRUD
│   ├── tryon_handler.go     # Virtual try-on endpoints
│   ├── gallery_handler.go   # Gallery management
│   ├── wardrobe_handler.go  # Wardrobe management
│   ├── theme_handler.go     # Theme retrieval
│   ├── feedback_handler.go  # Feedback submission
│   ├── legal_handler.go     # Privacy policy & terms
│   └── product_upload_handler.go  # Image-based product upload
├── config/
│   └── config.go            # Environment configuration
├── models/                  # MongoDB document models
│   ├── user.go
│   ├── person.go
│   ├── product.go
│   ├── tryon.go
│   ├── wardrobe.go
│   ├── theme.go
│   └── feedback.go
├── scrapers/                # E-commerce site scrapers
│   ├── interface.go         # Scraper interface definition
│   ├── factory.go           # Scraper selection logic
│   ├── base/                # Base scraper with HTTP/chromedp/Selenium
│   ├── amazon/
│   ├── flipkart/
│   ├── myntra/
│   ├── tatacliq/
│   └── peterengland/
├── utils/                   # Shared utilities
│   ├── mongo.go             # MongoDB connection
│   ├── s3.go                # AWS S3 operations
│   ├── token.go             # JWT generation/validation
│   ├── email.go             # SendGrid email
│   ├── gemini_client.go     # Gemini AI integration
│   ├── downloader.go        # Image downloader
│   ├── url_helper.go        # URL resolution
│   ├── api_helpers.go       # Response helpers
│   └── utils.go             # General utilities
├── static/                  # Landing page
├── docs/
│   └── API_DOCUMENTATION.md
├── Dockerfile
├── docker-compose.yml
├── Caddyfile
└── .github/workflows/
    └── deploy.yml           # CI/CD pipeline
```

## Prerequisites

- Go 1.24+
- MongoDB (local or Atlas)
- AWS account with S3 bucket
- Google Cloud project with Gemini API enabled
- SendGrid account for transactional emails
- Chromium & ChromeDriver (for scraping; included in Docker image)

## Environment Variables

Create a `.env` file in the project root:

```env
# Server
PORT=8080

# Database
MONGO_URI=mongodb://localhost:27017/
DB_NAME=fitly

# Authentication
JWT_SECRET=your_jwt_secret
GOOGLE_CLIENT_ID=your_google_client_id
GOOGLE_CLIENT_SECRET=your_google_client_secret

# AI
GEMINI_API_KEY=your_gemini_api_key

# AWS S3
AWS_REGION=ap-south-1
AWS_BUCKET_NAME=tryonfusion
AWS_ACCESS_KEY_ID=your_access_key
AWS_SECRET_ACCESS_KEY=your_secret_key

# Email
SENDGRID_API_KEY=your_sendgrid_api_key

# Contact
CONTACT_EMAIL=support@tryonfusion.com
```

## Getting Started

### Local Development

```bash
# Clone the repository
git clone https://github.com/raushankrgupta/web-product-scraper.git
cd web-product-scraper

# Install dependencies
go mod download

# Set up environment variables
cp .env.example .env
# Edit .env with your credentials

# Run the server
go run main.go
```

The server starts on `http://localhost:8080`.

### Docker

```bash
# Build the image
docker build -t tryonfusion-backend .

# Run with Docker Compose (includes Caddy reverse proxy)
docker-compose up -d
```

## API Endpoints

### Public Routes

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/auth/signup` | Register new user with OTP |
| POST | `/auth/verify-otp` | Verify email OTP |
| POST | `/auth/login` | Login with email/password |
| POST | `/auth/google` | Google OAuth login |
| POST | `/auth/forgot-password` | Request password reset OTP |
| POST | `/auth/reset-password` | Reset password with OTP |
| GET | `/legal/privacy-policy` | Get privacy policy |
| GET | `/legal/terms-of-service` | Get terms of service |
| GET | `/themes` | List try-on themes |

### Protected Routes (require `Authorization: Bearer <token>`)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/auth/change-password` | Change password |
| DELETE | `/auth/delete-account` | Soft-delete account |
| POST | `/product/details` | Scrape product from URL |
| POST | `/product/upload` | Upload product images |
| GET/POST | `/persons` | List or create persons |
| GET/PUT/DELETE | `/persons/{id}` | Person CRUD by ID |
| POST | `/try-on` | Legacy try-on (person + product) |
| POST | `/try-on/individual` | Individual try-on |
| POST | `/try-on/couple` | Couple try-on |
| POST | `/try-on/group` | Group try-on |
| GET | `/gallery` | Get gallery (paginated) |
| DELETE | `/gallery/{id}` | Delete gallery item |
| POST | `/gallery/{id}/favorite` | Toggle favorite |
| POST | `/gallery/{id}/save` | Mark as saved |
| POST | `/gallery/{id}/feedback` | Submit try-on feedback |
| GET/POST | `/wardrobe` | List or save wardrobe items |
| PUT | `/wardrobe/{id}` | Update wardrobe item category |
| DELETE | `/wardrobe/{id}` | Remove wardrobe item |
| POST | `/wardrobe/{id}/favorite` | Toggle wardrobe favorite |
| POST | `/feedback` | Submit app feedback |

See [API Documentation](docs/API_DOCUMENTATION.md) for detailed request/response formats.

## Supported E-Commerce Sites

| Site | Scraping Method |
|------|----------------|
| Amazon India | HTTP + goquery |
| Flipkart | HTTP + goquery |
| Myntra | chromedp (headless browser) |
| TataCliq | chromedp |
| Peter England | chromedp |

## Deployment

The project includes CI/CD via GitHub Actions (`.github/workflows/deploy.yml`) that deploys to an EC2 instance on push to `master`/`main`. See `deployment_guide_ec2.md` for full setup instructions.

**Production architecture:**
- Docker container running the Go server on port 8080
- Caddy reverse proxy handling HTTPS termination for `tryonfusion.com`
- MongoDB Atlas for database
- AWS S3 for image storage

## MongoDB Collections

| Collection | Purpose |
|------------|---------|
| `users` | User accounts, OTP, status |
| `person` | Person profiles (body measurements, images) |
| `products` | Scraped/uploaded products |
| `wardrobe` | Saved wardrobe items |
| `tryons` | Virtual try-on results |
| `themes` | Try-on themes |
| `feedbacks` | User feedback |

## License

This project is proprietary. All rights reserved.
