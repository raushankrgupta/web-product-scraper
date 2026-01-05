# API Documentation

## POST /create-profile

Creates a new user profile with body dimensions and uploaded images.

### Request

**Method:** `POST`
**Content-Type:** `multipart/form-data`

#### Form Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | Yes | Full name of the user |
| `age` | integer | No | Age of the user |
| `gender` | string | No | Gender (e.g., "male", "female") |
| `height` | float | No | Height in cm |
| `weight` | float | No | Weight in kg |
| `chest` | float | No | Chest size in inches |
| `waist` | float | No | Waist size in inches |
| `hips` | float | No | Hips size in inches |
| `images` | file | No | One or more image files to upload |

### Response

**Status Code:** `200 OK`
**Content-Type:** `application/json`

```json
{
  "message": "Profile created successfully",
  "id": "60d5ecb8b5c9c62b3c7c4b5a",
  "person": {
    "id": "000000000000000000000000",
    "name": "John Doe",
    "age": 30,
    "gender": "male",
    "height": 180,
    "weight": 75,
    "chest": 40,
    "waist": 32,
    "hips": 38,
    "image_paths": [
      "/home/user/project/user_images/1624621234567_photo.jpg"
    ]
  }
}
```

### Example Usage (cURL)

```bash
curl -X POST http://localhost:8080/create-profile \
  -F "name=John Doe" \
  -F "age=30" \
  -F "gender=male" \
  -F "height=180" \
  -F "weight=75" \
  -F "chest=40" \
  -F "waist=32" \
  -F "hips=38" \
  -F "images=@/path/to/image1.jpg" \
  -F "images=@/path/to/image2.jpg"
```
