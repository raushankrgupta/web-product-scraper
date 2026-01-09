# Profile API Documentation

## Overview
The Profile API handles creation, retrieval, updates, and deletion of user profiles ("persons"). A user can have multiple profiles (e.g., for themselves, family members).

## Base URL
`http://localhost:8081`

## Endpoints

### 1. Create Person
**Endpoint:** `POST /persons`
**Content-Type:** `multipart/form-data`
**Authentication:** Required (Bearer Token)

**Form Fields:**
- `name` (string, required)
- `age` (int)
- `gender` (string)
- `height` (float, cm)
- `weight` (float, kg)
- `chest` (float, cm)
- `waist` (float, cm)
- `hips` (float, cm)
- `images` (file, multiple allowed)

**Response (200 OK):**
Returns the created person object.

---

### 2. Get All Persons
**Endpoint:** `GET /persons`
**Authentication:** Required (Bearer Token)

**Response (200 OK):**
Returns a list of all persons belonging to the user.

---

### 3. Get Person By ID
**Endpoint:** `GET /persons/{id}`
**Authentication:** Required (Bearer Token)

**Response (200 OK):**
Returns the person details. Image paths are converted to presigned URLs.

---

### 4. Update Person
**Endpoint:** `PUT /persons/{id}`
**Content-Type:** `multipart/form-data`
**Authentication:** Required (Bearer Token)

**Description:** Updates details for a specific person. If `images` are provided, they **replace** the existing images. Fields left empty in the form will not modify existing values (except if the logic dictates replacement, currently updates provided fields).

**Form Fields:**
- `name` (string, optional)
- `age` (int, optional)
- ... (other measurements)
- `images` (file, optional - replaces existing if provided)

**Response (200 OK):**
Returns the updated person object.

---

### 5. Delete Person
**Endpoint:** `DELETE /persons/{id}`
**Authentication:** Required (Bearer Token)

**Response (204 No Content):**
Successful deletion.
