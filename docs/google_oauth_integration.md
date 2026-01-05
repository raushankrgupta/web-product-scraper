# Google OAuth2 Integration Guide

This guide explains how to integrate the Google OAuth2 login flow with the frontend.

## Prerequisites

Ensure the backend is running and the following environment variables are set in your `.env` file:

```env
GOOGLE_CLIENT_ID=your_google_client_id
GOOGLE_CLIENT_SECRET=your_google_client_secret
GOOGLE_REDIRECT_URL=http://localhost:8080/auth/google/callback
```

## API Endpoints

### 1. Initiate Login

**Endpoint:** `/auth/google/login`
**Method:** `GET`

**Description:**
Redirects the user to Google's OAuth2 consent screen.

**Frontend Usage:**
Create a "Login with Google" button that simply links to this endpoint.

```html
<a href="http://localhost:8080/auth/google/login">Login with Google</a>
```

### 2. Callback

**Endpoint:** `/auth/google/callback`
**Method:** `GET`

**Description:**
Google redirects back to this URL with a `code` and `state`. The backend exchanges the code for an access token and retrieves the user's profile information.

**Response:**
Currently, the backend returns the raw user profile JSON from Google.

```json
{
  "id": "1234567890",
  "email": "user@example.com",
  "verified_email": true,
  "name": "John Doe",
  "given_name": "John",
  "family_name": "Doe",
  "picture": "https://lh3.googleusercontent.com/..."
}
```

## Integration Flow

1.  **User Clicks Login**: User clicks the "Login with Google" button on your frontend.
2.  **Redirect**: The browser is redirected to Google's login page.
3.  **Authentication**: User logs in and grants permission.
4.  **Callback**: Google redirects the user back to `/auth/google/callback`.
5.  **Handling Response**:
    *   **Simple Integration**: The browser will display the JSON response.
    *   **Advanced Integration**: You might want to update the callback handler to redirect to a frontend URL with a session token (JWT) after successful authentication, instead of just dumping the JSON.

## Notes

-   **State Parameter**: The current implementation uses a static "random-state" string. for production, this should be a randomly generated string stored in a session/cookie to prevent CSRF attacks.
