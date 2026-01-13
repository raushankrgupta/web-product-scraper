# Fitly API Documentation

## Overview
This API provides backend services for the Fitly application, including user authentication, profile management, product scraping, and virtual try-on features.

**Base URL**: `http://localhost:8081` (default)

## Authentication

Authentication is token-based (JWT). Include the token in the `Authorization` header for protected routes:
```
Authorization: Bearer <your_token>
```

### 1. Signup
- **Endpoint**: `POST /auth/signup`
- **Description**: Register a new user and send OTP.
- **Body**:
  ```json
  {
    "name": "John Doe",
    "email": "john@example.com",
    "password": "securepassword",
    "dob": "1990-01-01",
    "gender": "Male"
  }
  ```
- **Response**: `201 Created`

### 2. Verify OTP
- **Endpoint**: `POST /auth/verify-otp`
- **Description**: Verify email or password reset OTP.
- **Body**:
  ```json
  {
    "email": "john@example.com",
    "otp": "123456"
  }
  ```
- **Response**: `200 OK`

### 3. Login
- **Endpoint**: `POST /auth/login`
- **Description**: Login and receive JWT token.
- **Body**:
  ```json
  {
    "email": "john@example.com",
    "password": "securepassword"
  }
  ```
- **Response**: `200 OK`
  ```json
  {
      "message": "Login successful",
      "token": "...",
      "user": { ... }
  }
  ```

### 4. Forgot Password
- **Endpoint**: `POST /auth/forgot-password`
- **Body**: `{ "email": "john@example.com" }`

### 5. Reset Password
- **Endpoint**: `POST /auth/reset-password`
- **Body**:
  ```json
  {
    "email": "john@example.com",
    "otp": "123456",
    "new_password": "newpassword"
  }
  ```

### 6. Change Password (Protected)
- **Endpoint**: `POST /auth/change-password`
- **Headers**: `Authorization: Bearer <token>`
- **Body**:
  ```json
  {
    "current_password": "oldpassword",
    "new_password": "newpassword"
  }
  ```

---

## Person Profiles (Protected)

Manage user profiles ("persons").

### 1. Create Person
- **Endpoint**: `POST /persons`
- **Type**: `multipart/form-data`
- **Fields**: `name`, `age`, `gender`, `height`, `weight`, `chest`, `waist`, `hips`, `images` (file).
- **Response**: `201 Created` (returns created person object).

### 2. Get All Persons
- **Endpoint**: `GET /persons`
- **Response**: `200 OK` (list of persons).

### 3. Get Person By ID
- **Endpoint**: `GET /persons/{id}`
- **Response**: `200 OK` (person details).

### 4. Update Person
- **Endpoint**: `PUT /persons/{id}`
- **Type**: `multipart/form-data`
- **Fields**: Optional updates (`name`, `age`, `images`, etc.).
- **Response**: `200 OK` (updated person object).

### 5. Delete Person
- **Endpoint**: `DELETE /persons/{id}`
- **Response**: `204 No Content` (actually returns 204 status with no body, currently implemented as such or similar).

---

## Products & Scraping (Protected)

### 1. Scrape Product
- **Endpoint**: `POST /product/details`
- **Body**:
  ```json
  {
    "url": "https://example.com/product"
  }
  ```
  (Can also use query param `?url=...` with GET/POST)
- **Response**: `200 OK` (returns scraped product details including images).

---

## Virtual Try-On (Protected)

### 1. Generate Try-On
- **Endpoint**: `POST /try-on`
- **Body**:
  ```json
  {
    "product_id": "<mongodb_product_id>",
    "person_id": "<mongodb_person_id>"
  }
  ```
- **Response**: `200 OK`
  ```json
  {
      "result": "<presigned_url_of_generated_image>",
      "tryon_details": { ... }
  }
  ```

---

## Gallery (Protected)

### 1. Get Generated Images
- **Endpoint**: `GET /gallery`
- **Query Params**: `page` (default 1), `limit` (default 10).
- **Response**: `200 OK`
  ```json
  {
      "images": [ ... ],
      "total": 100,
      "current_page": 1,
      "total_pages": 10
  }

### 2. Delete Generated Image
- **Endpoint**: `DELETE /gallery/{id}`
- **Response**: `204 No Content`
  ```
