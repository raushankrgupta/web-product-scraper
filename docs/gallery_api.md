# Gallery API Documentation

## Overview
The Gallery API allows users to retrieve their generated virtual try-on images. It supports pagination and returns images in descending order of creation (newest first).

## Base URL
`http://localhost:8081` (assuming default port from config)

## Endpoint

### Get Gallery Images
**Endpoint:** `GET /gallery`
**Method:** `GET`
**Authentication:** Required (Bearer Token)

**Query Parameters:**
| Parameter | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `page` | `int` | `1` | Page number to retrieve. |
| `limit` | `int` | `10` | Number of items per page. |

**Request Example:**
```
GET /gallery?page=1&limit=5
Authorization: Bearer <your_jwt_token>
```

**Response (200 OK):**
```json
{
  "images": [
    {
      "id": "64f8a...",
      "user_id": "64f1d...",
      "person_id": "64f8a...",
      "product_url": "https://www.amazon.com/...",
      "person_image_url": "https://s3.amazonaws.com/...",
      "generated_image_url": "https://s3.amazonaws.com/...",
      "status": "completed",
      "created_at": "2023-09-06T12:00:00Z"
    },
    ...
  ],
  "total": 15,
  "current_page": 1,
  "total_pages": 3
}
```
