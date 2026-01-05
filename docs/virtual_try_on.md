# Virtual Try-On Feature Guide

This guide explains how to use the Virtual Try-On API.

## Prerequisites

Ensure the backend is running and `GEMINI_API_KEY` is set in your `.env` file.

## API Endpoint

### Generate Try-On Image

**Endpoint:** `/try-on`
**Method:** `POST`
**Content-Type:** `application/json`

**Request Body:**

```json
{
  "product_url": "https://www.amazon.in/dp/B0...",
  "person_id": "64f8a..."
}
```

-   `product_url`: The URL of the product to try on (e.g., Amazon product page).
-   `person_id`: The MongoDB ID of the person profile to use.

**Response:**

```json
{
  "result": "..."
}
```

The `result` field will contain the output from Gemini. This could be a description or a URL to the generated image, depending on the model's response.

## Integration Flow

1.  **User Selects Product**: User provides a product URL.
2.  **User Selects Profile**: User selects their profile (Person ID).
3.  **Frontend Request**: Frontend sends a POST request to `/try-on`.
4.  **Backend Processing**:
    -   Backend scrapes the product details.
    -   Backend fetches the person's details and image.
    -   Backend sends both to Gemini API with a prompt to generate a try-on image.
5.  **Display Result**: Frontend displays the generated result.

## Notes

-   **Image Access**: The backend currently expects the person's image path to be a publicly accessible URL for Gemini to access it (if using URL-based processing) or handles it internally. Ensure `person.image_paths` contains valid URLs.
-   **Model**: The integration uses `gemini-1.5-pro`.
