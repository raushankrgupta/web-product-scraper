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

### 7. Delete Account (Protected)
- **Endpoint**: `DELETE /auth/delete-account`
- **Headers**: `Authorization: Bearer <token>`
- **Response**: `200 OK`
  ```json
  {
      "message": "Account deleted successfully. You have been logged out."
  }
  ```

---

## Legal (Public)

### 1. Get Privacy Policy
- **Endpoint**: `GET /legal/privacy-policy`
- **Response**: `200 OK`
  ```json
  {
      "content": "# Privacy Policy\n..."
  }
  ```

### 2. Get Terms of Service
- **Endpoint**: `GET /legal/terms-of-service`
- **Response**: `200 OK`
  ```json
  {
      "content": "# Terms of Service\n..."
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

## App Config (Public)

### 1. Get App Config
- **Endpoint**: `GET /app/config`
- **Auth**: none. Cached `public, max-age=300`, supports `If-None-Match`/`ETag`.
- **Purpose**: remote switchboard for Link Import (which fetch path a client uses, limits, blocked hosts). Clients embed defaults for every field and tolerate unknown fields.
- **Response**: `200 OK`
  ```json
  {
    "schema": 1,
    "generated_at": "2026-09-07T10:00:00Z",
    "min_app_version": "2.3.4",
    "link_import": {
      "mode": "device",
      "max_images": 6,
      "max_candidates": 60,
      "min_image_px": 200,
      "upload_max_edge": 1024,
      "jpeg_quality": 0.7,
      "blocked_hosts": [],
      "notice_version": 1
    },
    "server_scrape": { "mode": "deprecated", "sunset": "2026-11-30" }
  }
  ```
  Env: `LINK_IMPORT_MODE`, `SERVER_SCRAPE_MODE`, `SERVER_SCRAPE_SUNSET`, `LINK_IMPORT_BLOCKED_HOSTS`, `LINK_IMPORT_MAX_IMAGES`, `LINK_IMPORT_MAX_CANDIDATES`, `LINK_IMPORT_NOTICE_VERSION`, `MIN_APP_VERSION`.

---

## Tutorials (Public)

### 1. List Tutorial Videos
- **Endpoint**: `GET /tutorials`
- **Auth**: none. Cached `public, max-age=300`, supports `ETag`.
- **Source**: the TryOnFusion YouTube channel's public Atom feed (`YOUTUBE_CHANNEL_ID`), re-read every `TUTORIALS_CACHE_SECS` (default 3600). New uploads appear automatically; `TUTORIALS_HIDDEN_VIDEO_IDS` hides specific videos.
- **Response**: `200 OK`
  ```json
  {
    "channel": { "id": "UC…", "handle": "@tryonfusion", "title": "TryOnFusion: Virtual Try-On", "url": "https://www.youtube.com/@tryonfusion" },
    "videos": [
      { "id": "kQAttI5WVzw", "title": "TryOnFusion: Demo", "description": "…", "published_at": "2026-05-25T19:08:12Z",
        "updated_at": "…", "thumbnail_url": "https://i4.ytimg.com/vi/kQAttI5WVzw/hqdefault.jpg",
        "url": "https://www.youtube.com/shorts/kQAttI5WVzw",
        "embed_url": "https://www.youtube-nocookie.com/embed/kQAttI5WVzw?playsinline=1&rel=0&modestbranding=1",
        "is_short": true, "views": 11 }
    ],
    "fetched_at": "…",
    "stale": false
  }
  ```
- `503` only when the feed has never been fetched successfully in this process.

---

## Products (Protected)

Clients send `X-App-Version` and `X-Platform` headers from app 2.4.0. Their absence identifies a legacy client.

### 1. Upload Product Images
- **Endpoint**: `POST /product/upload` (multipart/form-data)
- **Fields**:
  - `images` — 1..8 files (JPEG/PNG/WebP, validated by magic bytes; 15 MB body cap)
  - `source` — optional: `user_upload` (default) | `link_import`
  - `source_url` — required when `source=link_import`. Stored as a reference (tracking params stripped); **never fetched by the server**.
  - `title` — optional, ≤ 200 chars
  - `import_method` — optional analytics label: `dom_pick | tap | in_page_fetch | screenshot | mixed`
  - `page_host` — optional; derived from `source_url` when absent
- **Response**: `201 Created` with the Product document (presigned `image_paths`).
- **Errors**: `400 {"reason":"invalid_request"}` (bad `source`/`source_url`), `400 {"reason":"too_many_images"}`, `429 {"reason":"rate_limited"}` (60 uploads/hour/user).

### 2. Scrape Product (LEGACY — clients ≤ 2.3.4 only)
- **Endpoint**: `POST /product/details`
- **Body**:
  ```json
  {
    "url": "https://example.com/product"
  }
  ```
  (Can also use query param `?url=...` with GET/POST)
- **Response**: `200 OK` (returns scraped product details including images).
- **Gating** (`SERVER_SCRAPE_MODE`):
  - `enabled` — as before
  - `deprecated` (default) — as before, plus `Deprecation: true` and `Sunset: <date>` headers
  - `disabled` — `410 Gone {"error":"…update TryOnFusion…","reason":"update_required"}`. Legacy apps map the unknown reason to `scrape_failed` and offer their screenshot-upload path.
- New clients in `link_import.mode = "device"` never call this endpoint. The same gate applies to a url-only `POST /try-on/guest`; when `product_image` is present the URL is stored as a reference and **not** scraped.

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

---

## Feedback & Support (Protected)

### 1. Submit Feedback
- **Endpoint**: `POST /feedback`
- **Type**: `multipart/form-data`
- **Fields**:
    - `name` (text, required)
    - `email` (text, required)
    - `message` (text, required)
    - `country_code` (text, optional)
    - `mobile_number` (text, optional)
    - `contact_back` (boolean, optional, default: `false`)
    - `files` (file, optional, multiple supported)
- **Response**: `201 Created`
  ```json
  {
      "message": "Feedback submitted successfully"
  }
  ```
