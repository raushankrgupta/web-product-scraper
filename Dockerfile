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

# 3. Ensure Chromium user-data dirs exist (Crucial for chromedp). The Myntra
# scraper uses its own profile dir (/tmp/chrome-user-data-myntra) so it can
# run a concurrent Chromium instance without colliding on Chromium's exclusive
# lock against scrapers/base's /tmp/chrome-user-data.
RUN mkdir -p /tmp/chrome-user-data /tmp/chrome-user-data-myntra && \
    chmod 777 /tmp/chrome-user-data /tmp/chrome-user-data-myntra

# Set working directory
WORKDIR /app

# Copy go.mod and go.sum files first to leverage Docker cache
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application.
#
# APP_VERSION is injected into main.version, which tags every Telegram alert
# and is reported by /health — so an alert can be traced back to the exact
# build that emitted it. CI passes the git sha; a plain `docker build` gets
# "dev".
ARG APP_VERSION=dev
RUN go build -ldflags "-X main.version=${APP_VERSION}" -o web-product-scraper .

# Create non-root user for security
RUN useradd -m -u 1000 appuser && \
    chown -R appuser:appuser /app /tmp/chrome-user-data /tmp/chrome-user-data-myntra

# Expose the port
EXPOSE 8080

# Environment variable to help Chromedp find the binary
ENV CHROME_BIN=/usr/bin/chromium
ENV CHROMEDRIVER_PATH=/usr/bin/chromedriver

USER appuser

# Command to run the executable
CMD ["./web-product-scraper"]
