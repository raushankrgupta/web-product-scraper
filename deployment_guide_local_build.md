# Deployment Guide: Local Build & Deploy

This guide explains how to build the application Docker image **on your local machine** and deploy it to your remote EC2 server. This approach avoids "no space left on device" errors on small servers.

## Prerequisites

1.  **Docker Hub Account:** You need an account at [hub.docker.com](https://hub.docker.com/).
    *   If you don't have one, create it (it's free).
2.  **Local Docker:** Docker must be running on your local machine.
3.  **Remote Docker:** Docker must be running on your EC2 server (already set up).

---

## Step 1: Login to Docker Hub (Local Machine)

Open your terminal on your **local machine** and run:

```bash
docker login
```
Enter your Docker Hub username and password.

---

## Step 2: Build & Push the Image (Local Machine)

1.  **Build** the image with your Docker Hub username as the tag prefix:
    *   Replace `<your-dockerhub-username>` with your actual username.
    ```bash
    # Example: docker build -t johndoe/web-product-scraper:latest .
    docker build -t <your-dockerhub-username>/web-product-scraper:latest .
    ```

2.  **Push** the image to Docker Hub:
    ```bash
    docker push <your-dockerhub-username>/web-product-scraper:latest
    ```
    *This uploads the built image to the cloud.*

---

## Step 3: Update `docker-compose.yml` (Remote Server)

You need to tell the server to pull this new image instead of trying to build it.

1.  **SSH into your server:**
    ```bash
    # Use your specific key and IP
    ssh -i "tryon.pem" ubuntu@ip-172-31-10-2
    ```

2.  **Edit the file:**
    ```bash
    cd ~/web-product-scraper
    nano docker-compose.yml
    ```

3.  **Modify the `app` service:**
    *   Change `build: .` to `image: phikarnot/web-product-scraper:latest`.
    *   **Comment out** or delete the `build: .` line.

    **Before:**
    ```yaml
    services:
      app:
        build: .
        container_name: web-product-scraper
        # ...
    ```

    **After:**
    ```yaml
    services:
      app:
        image: <your-dockerhub-username>/web-product-scraper:latest  # <--- Change this
        # build: .  <--- Comment this out
        container_name: web-product-scraper
        # ...
    ```

4.  **Save and Exit:** Press `Ctrl+O`, `Enter`, then `Ctrl+X` (if using nano).

---

## Step 4: Deploy (Remote Server)

Now deploy using the pre-built image.

1.  **Pull the latest image:**
    ```bash
    docker compose pull
    ```

2.  **Start the services:**
    ```bash
    docker compose up -d --remove-orphans
    ```

The server will download the image (which is compressed and much smaller than the build files) and start it immediately. No build process will run on the server!
