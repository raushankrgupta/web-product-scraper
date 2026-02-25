# Start with Go base image (Debian-based)
FROM golang:1.24

# 1. Install Chromium and the Driver
# This installs both the browser and the matching driver automatically.
RUN apt-get update && apt-get install -y \
    ca-certificates \
    chromium \
    chromium-driver \
    && rm -rf /var/lib/apt/lists/*

# 2. (Optional) Create a Symlink if you don't want to change your Go code
# apt installs specific versions, so we symlink to a known location if needed
# RUN ln -s /usr/bin/chromedriver /usr/local/bin/chromedriver

# 3. Ensure /tmp/chrome-user-data exists (Crucial for chromedp)
RUN mkdir -p /tmp/chrome-user-data && chmod 777 /tmp/chrome-user-data

# Set working directory
WORKDIR /app

# Copy go.mod and go.sum files first to leverage Docker cache
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application
RUN go build -o web-product-scraper .

# Expose the port
EXPOSE 8080

# Environment variable to help Chromedp find the binary
ENV CHROME_BIN=/usr/bin/chromium
ENV CHROMEDRIVER_PATH=/usr/bin/chromedriver

# Command to run the executable
CMD ["./web-product-scraper"]
