# Deployment Guide: AWS EC2 (Free Tier)

This guide walks you through deploying your Go application to an AWS EC2 instance using Docker.

## Prerequisites
*   An AWS Account.
*   Your project code pushed to a Git repository (GitHub/GitLab/Bitbucket) OR you can upload it manually.

---

## Step 0: Configure DNS (Crucial for HTTPS)

1.  Go to your Domain Registrar (Namecheap, GoDaddy, AWS Route53).
2.  Find the **DNS Management** section for `tryonfusion.com`.
3.  Create/Update these **A Records**:
    *   **Host**: `@` (or `tryonfusion.com`) -> **Value**: `13.233.10.157`
    *   **Host**: `www` -> **Value**: `13.233.10.157`
4.  Wait a few minutes for propagation.

---

## Step 1: Launch an EC2 Instance

1.  Log in to the AWS Console and search for **EC2**.
2.  Click **Launch Instance**.
3.  **Name**: `Go-App-Server`
4.  **OS Images**: Choose **Ubuntu Server 24.04 LTS** (Free Tier eligible).
5.  **Instance Type**: Choose **t2.micro** or **t3.micro** (Free Tier eligible).
6.  **Key Pair**: Create a new key pair (e.g., `ec2-key`), download the `.pem` file, and keep it safe.
7.  **Network Settings**:
    *   Check "Allow SSH traffic from Anywhere" (or My IP for better security).
    *   Check "Allow HTTPS traffic from the internet".
    *   Check "Allow HTTP traffic from the internet".
8.  Click **Launch Instance**.

---

## Step 2: Connect to Your Instance

1.  Open your terminal where your `.pem` key is located.
2.  Set permissions: `chmod 400 ec2-key.pem`
3.  Connect via SSH (replace `YOUR_PUBLIC_IP` with the EC2 IP address from the console):
    ```bash
    ssh -i "ec2-key.pem" ubuntu@YOUR_PUBLIC_IP
    ```

---

## Step 3: Install Docker & Git

Run the following commands inside your EC2 terminal:

```bash
# Update package list
sudo apt-get update

# Install Docker
sudo apt-get install -y docker.io docker-compose-v2 git

# Start Docker and enable it to run on boot
sudo systemctl start docker
sudo systemctl enable docker

# Add your user to the docker group (avoids typing sudo for every docker command)
sudo usermod -aG docker $USER
```
*Note: After the last command, type `exit` to disconnect, then SSH back in for the permission change to take effect.*

---

## Step 4: Clone Your Code

```bash
# Clone your repository (use HTTPS or setup SSH keys if private)
git clone https://github.com/YOUR_USERNAME/web-product-scraper.git

# Enter the directory
cd web-product-scraper
```

---

## Step 5: Configure Environment Variables

Create your production `.env` file:

```bash
nano .env
```

Paste your environment variables. **CRITICAL**: Make sure to set `MONGO_URI` correctly.
*   If using **MongoDB Atlas**: `MONGO_URI=mongodb+srv://user:pass@cluster...`


```env
MONGO_URI=your_mongo_uri_here
PORT=8080
GOOGLE_CLIENT_ID=your_id
GOOGLE_CLIENT_SECRET=your_secret
GOOGLE_REDIRECT_URL=http://YOUR_EC2_PUBLIC_IP/auth/google/callback
GEMINI_API_KEY=your_key
AWS_REGION=your_region
AWS_BUCKET_NAME=your_bucket
AWS_ACCESS_KEY_ID=your_aws_key
AWS_SECRET_ACCESS_KEY=your_aws_secret
```
*Press `Ctrl+O`, `Enter` to save, and `Ctrl+X` to exit.*

---

## Step 6: Start the Application

Build and start the containers in the background:

```bash
docker compose up -d --build
```

### Verification
*   Check if containers are running: `docker compose ps`
*   View logs: `docker compose logs -f`

---

---

## Step 7: Access Your App

Open your browser and visit:
`https://tryonfusion.com`


**First Time Note**: It might take a few seconds for the green padlock to appear while Caddy negotiates the certificate.

Your Go API is now live and secured with HTTPS! 🚀

---

## Step 8: Automating with GitHub Actions

To enable auto-deployment whenever you push to GitHub:

1.  **Go to your GitHub Repository** -> **Settings** -> **Secrets and variables** -> **Actions**.
2.  Click **New repository secret**.
3.  Add the following secrets:
    *   `EC2_HOST`: Your Public IP (`13.233.10.157` or `tryonfusion.com`)
    *   `EC2_USER`: `ubuntu`
    *   `EC2_SSH_KEY`: Open your `.pem` key file, copy **everything** (including `-----BEGIN RSA PRIVATE KEY-----`), and paste it here.
4.  Push a change to the `master` branch. You can watch the "Actions" tab in GitHub to see it deploy automatically!

