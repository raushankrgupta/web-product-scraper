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
  "product_id": "507f1f77bcf86cd799439011",
  "person_id": "64f8a..."
}
```

-   `product_id`: (Required) The MongoDB ID of a previously scraped product.
-   `person_id`: (Required) The MongoDB ID of the person profile to use.

**Response:**

```json
{
  "result": "..."
}
```

The `result` field will contain the output from Gemini. This could be a description or a URL to the generated image, depending on the model's response.

## Integration Flow

1.  **Scrape Product**: Call `POST /product/details` with product URL.
2.  **Save Product ID**: Store the returned `product.id`.
3.  **User Selects Profile**: User selects their profile (Person ID).
4.  **Try-On Request**: Call `POST /try-on` with `product_id` and `person_id`.
5.  **Display Result**: Frontend displays the generated result.


## Notes

-   **Performance**: Using `product_id` is significantly faster as it avoids re-scraping the product.
-   **Image Access**: The backend currently expects the person's image path to be a publicly accessible URL for Gemini to access it (if using URL-based processing) or handles it internally. Ensure `person.image_paths` contains valid URLs.
-   **Model**: The integration uses `gemini-1.5-pro`.
