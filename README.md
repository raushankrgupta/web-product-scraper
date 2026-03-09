# Fitly Backend (web-product-scraper)

This is the main Go backend for the Fitly app. It handles authentication, profile management, and coordinates the 3D-Profile Reconstruction worker.

## Setup Instructions

1. **Environment Variables (`.env`)**
   Ensure your `.env` file contains all necessary configuration keys. Crucially, verify that `JWT_SECRET` is set correctly:
   ```env
   MONGO_URI="mongodb+srv://..."
   PORT=8080
   JWT_SECRET="..."
   AWS_ACCESS_KEY_ID="..."
   AWS_SECRET_ACCESS_KEY="..."
   AWS_REGION="ap-south-1"
   AWS_BUCKET_NAME="tryonfusion"
   # Points to the Python reconstruction service:
   RECONSTRUCTION_SERVICE_URL="http://localhost:8000"
   ```

2. **Run the Backend locally:**
   ```bash
   go run main.go
   ```
   Or optionally build and run the binary:
   ```bash
   go build -o bin/server main.go
   ./bin/server
   ```

3. **Expose with ngrok (for Mobile App access):**
   When running the mobile app locally via Expo, your app requires a publicly accessible URL to reach this backend securely.
   Use ngrok to expose your local port:
   ```bash
   ngrok http 8080
   ```
   *Copy the generated ngrok URL and paste it into `fitly-app/.env` as `EXPO_PUBLIC_API_BASE_URL`.*

## Subsystems

- **User Authentication:** Generates and validates JWTs based on `JWT_SECRET`. If clients show an `Invalid or expired token` error, this is usually because the `JWT_SECRET` rotated or was stripped from the environment.
- **3D Avatar Generation:** The backend accepts frontend image multipart uploads, drops them in S3, and forwards the job to `RECONSTRUCTION_SERVICE_URL`. Ensure the Python microservice is running concurrently for this to work.
