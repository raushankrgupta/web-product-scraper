# Frontend Integration Guide

This guide covers the integration with the Fitly Backend API, including Authentication, Person Management, and Fitting.

## Authentication
> **Important**: All protected endpoints require the JWT token returned from Login/Signup to be passed in the `Authorization` header.

**Header Format:**
`Authorization: Bearer <your_jwt_token>`

### 1. Signup
- **Endpoint**: `POST /auth/signup`
- **Body**: `{ "name": "...", "email": "...", "password": "...", "dob": "...", "gender": "..." }`
- **Response**: `201 Created`
  - Returns `user` object.
  - Sends OTP email to user.

### 2. Verify OTP
- **Endpoint**: `POST /auth/verify-otp`
- **Body**: `{ "email": "...", "otp": "..." }`
- **Response**: `200 OK`
  - Verifies email or password reset OTP.

### 3. Login
- **Endpoint**: `POST /auth/login`
- **Body**: `{ "email": "...", "password": "..." }`
- **Response**: `200 OK`
  ```json
  {
    "message": "Login successful",
    "token": "eyJhbGciOiJIUzI1Ni...",
    "user": { ... }
  }
  ```
  > **Action**: Store `token` securely (e.g., localStorage/SecureStore) and use it for subsequent protected requests.

### 4. Password Reset
- **Forgot Password**: `POST /auth/forgot-password` (`{ "email": "..." }`) -> Sends OTP.
- **Verify OTP**: `POST /auth/verify-otp` (Same as above).
- **Reset Password**: `POST /auth/reset-password` (`{ "email": "...", "otp": "...", "new_password": "..." }`) -> Resets password.

---

## Person Management (Protected)
> **Requires Auth Header**

### 1. Create Person Profile
- **Endpoint**: `POST /persons`
- **Content-Type**: `multipart/form-data`
- **Fields**:
  - `name` (string, required)
  - `age`, `height`, `weight`, `chest`, `waist`, `hips` (numeric)
  - `gender` (string)
  - `images` (file, multiple allowed)
- **Response**: `200 OK` - Returns created person object.

### 2. List Persons
- **Endpoint**: `GET /persons`
- **Response**: `200 OK` - JSON array of persons belonging to the user.

### 3. Get Person Details
- **Endpoint**: `GET /persons/:id`
- **Response**: `200 OK` - JSON object of person.

### 4. Delete Person
- **Endpoint**: `DELETE /persons/:id`
- **Response**: `204 No Content`

---

## Product & Fitting (Protected/Public?)
> **Note**: Currently `/product/details` checks for auth depending on implementation, but `/fitting/generate` is protected.

### 1. Get Product Details (Scrape)
- **Endpoint**: `POST /product/details`
- **Body**: `{ "url": "https://shop.com/product..." }`
- **Response**: `200 OK`
  ```json
  {
    "title": "...",
    "price": "...",
    "images": ["url1", "url2", ...],
    "variants": [...]
  }
  ```

### 2. Generate Fitting
- **Endpoint**: `POST /fitting/generate`
- **Body**:
  ```json
  {
    "product_id": "scraped_product_id_or_url?", // Currently logic accepts product_id but scraping returns data. frontend might need to pass product details or ID if stored. 
    // UPDATE: The backend currently mocks this. For now pass a dummy ID or the URL if the backend logic evolves.
    "person_id": "mongo_person_id"
  }
  ```
- **Response**: `200 OK`
  ```json
  {
    "fitting_score": 85,
    "feedback": "The fit looks good...",
    "visual_url": "..."
  }
  ```
