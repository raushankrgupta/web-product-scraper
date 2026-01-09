# Generic Authentication API Documentation

## Base URL
`http://localhost:8081` (assuming default port from config, adjust if necessary)

## Endpoints

### 1. Signup
**Endpoint:** `POST /auth/signup`
**Description:** Registers a new user and sends an OTP to their email.

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
  "message": "User registered successfully. Please verify your email using the OTP sent.",
  "user": {
    "name": "John Doe",
    "email": "john.doe@example.com",
    "status": "pending",
    ...
  }
}
```

---

### 2. Verify OTP (Email Verification & Password Reset)
**Endpoint:** `POST /auth/verify-otp`
**Description:** Verifies the OTP sent to the user's email. Used for both initial account verification and password reset flow.

**Request Body:**
```json
{
  "email": "john.doe@example.com",
  "otp": "123456"
}
```

**Response (200 OK):**
- **If used for Account Verification:**
```json
{
  "message": "Email verification successful! You can now login."
}
```
- **If used for Password Reset (User already verified):**
```json
{
  "message": "OTP verified successfully. Please proceed to reset password."
}
```

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
  "user": { ... }
}
```

**Response (403 Forbidden):**
- "Please verify your email first" (If account is still pending)

---

### 4. Forgot Password
**Endpoint:** `POST /auth/forgot-password`
**Description:** Initiates password recovery by sending an OTP to the registered email.

**Request Body:**
```json
{
  "email": "john.doe@example.com"
}
```

**Response (200 OK):**
```json
{
  "message": "OTP sent to your email."
}
```

---

### 5. Reset Password
**Endpoint:** `POST /auth/reset-password`
**Description:** Resets the user's password using the verified OTP.

**Request Body:**
```json
{
  "email": "john.doe@example.com",
  "otp": "123456",
  "new_password": "newSecurePassword123"
}
```

**Response (200 OK):**
```json
{
  "message": "Password reset successfully. Please login with your new password."
}
```

---

### 6. Change Password
**Endpoint:** `POST /auth/change-password`
**Description:** Allows a logged-in user to change their password by providing the current password.
**Authentication:** Required (Bearer Token)

**Request Body:**
```json
{
  "current_password": "oldSecurePassword123",
  "new_password": "newSuperSecurePassword456"
}
```

**Response (200 OK):**
```json
{
  "message": "Password changed successfully"
}
```

**Response (401 Unauthorized):**
- Invalid current password

