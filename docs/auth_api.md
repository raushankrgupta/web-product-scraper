# Generic Authentication API Documentation

## Base URL
`http://localhost:8081` (assuming default port from config, adjust if necessary)

## Endpoints

### 1. Signup
**Endpoint:** `POST /auth/signup`
**Description:** Registers a new user.

**Request Body:**
```json
{
  "name": "John Doe",
  "email": "john.doe@example.com",
  "password": "securepassword123",
  "dob": "1990-01-01",
  "gender": "Male"
}
```

**Response (201 Created):**
```json
{
  "message": "User registered successfully. Please accept the verification link sent to your email.",
  "user": {
    "id": "60d5ec49f1b2c820...",
    "name": "John Doe",
    "email": "john.doe@example.com",
    "dob": "1990-01-01",
    "gender": "Male",
    "status": "pending",
    "created_at": "2023-10-27T10:00:00Z",
    "updated_at": "2023-10-27T10:00:00Z"
  }
}
```

**Response (400 Bad Request):**
- Missing fields.
- Invalid format.

**Response (409 Conflict):**
- User with email already exists.

---

### 2. Verify Email
**Endpoint:** `GET /auth/verify-email`
**Description:** Verifies user's email using the token sent via email.

**Query Parameters:**
- `token` (required): The verification token.

**Response (200 OK):**
```json
{
  "message": "Email verification completed! Kindly proceed with login"
}
```

**Response (400 Bad Request):**
- Token invalid or expired.

---

### 3. Login
**Endpoint:** `POST /auth/login`
**Description:** Authenticates a user.

**Request Body:**
```json
{
  "email": "john.doe@example.com",
  "password": "securepassword123"
}
```

**Response (200 OK):**
```json
{
  "message": "Login successful",
  "user": {
    "id": "60d5ec49f1b2c820...",
    "name": "John Doe",
    "email": "john.doe@example.com",
    "dob": "1990-01-01",
    "gender": "Male",
    "created_at": "2023-10-27T10:00:00Z",
    "updated_at": "2023-10-27T10:00:00Z"
  }
}
```

**Response (401 Unauthorized):**
- Invalid email or password.

**Response (403 Forbidden):**
- Please verify your email first.

---

### 4. Forgot Password
**Endpoint:** `POST /auth/forgot-password`
**Description:** Initiates password recovery.

**Request Body:**
```json
{
  "email": "john.doe@example.com"
}
```

**Response (200 OK):**
```json
{
  "message": "If the email is registered, a password recovery link has been sent."
}
```

**Response (400 Bad Request):**
- Email is missing.
