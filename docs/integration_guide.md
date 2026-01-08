# Integration Guide

## Authentication
All API endpoints (except login/signup) require a Bearer Token in the `Authorization` header.

Header Format:
```
Authorization: Bearer <your_jwt_token>
```

---

## 1. Product Details & Scraping
**Endpoint:** `GET /product/details` or `POST /product/details`

**Description:** Scrapes product details from a given URL and saves them to the database.

**Request:**
- **Query Param:** `?url=<product_url>`
- **OR JSON Body:**
  ```json
  {
    "url": "https://example.com/product/123"
  }
  ```

**Response (Success - 200 OK):**
```json
{
  "id": "65e...",              // Product ID in MongoDB
  "user_id": "user_123...",    // ID of the user who requested
  "created_at": "2024-03-...",
  "title": "Product Title",
  "mrp": "Rs. 1999",
  "discounted_price": "Rs. 999",
  "discount": "50% off",
  "description": "Product detailed description...",
  "category": "Clothing",
  "dimensions": "10x20x5",
  "image_paths": [
    "http://localhost:8080/product_images/image1.jpg",
    "http://localhost:8080/product_images/image2.jpg"
  ],
  "variants": [...]
}
```

---

## 2. Virtual Try-On
**Endpoint:** `POST /try-on`

**Description:** Generates a virtual try-on image using the user's uploaded image and a scraped product.

**Request (JSON Body):**
```json
{
  "product_url": "https://example.com/product/123",
  "person_id": "65e..." // ID of the person profile (must exist)
}
```

**Response (Success - 200 OK):**
```json
{
  "result": "http://localhost:8080/user_images/generated_tryon_123456789.jpg",
  "tryon_details": {
    "id": "65f...",
    "user_id": "user_123...",
    "person_id": "65e...",
    "product_url": "https://example.com/product/123",
    "person_image_url": "...",
    "generated_image_url": "http://localhost:8080/user_images/generated_tryon_123456789.jpg",
    "status": "completed",
    "created_at": "..."
  }
}
```

---

## Notes
- Images are saved locally in `product_images/` and `user_images/` directories and served via static file handlers.
- Ensure the `person_id` provided in Try-On request belongs to a valid Person profile created via `/persons` API.
