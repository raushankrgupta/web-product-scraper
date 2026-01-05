# Start from a small, secure base image
FROM golang:1.24-alpine AS builder

# Install git and ca-certificates (needed for dependencies and HTTPS)
# Added retries for apk to handle transient network issues
RUN apk add --no-cache --update git ca-certificates || \
    (sleep 5 && apk add --no-cache --update git ca-certificates) || \
    (sleep 10 && apk add --no-cache --update git ca-certificates)

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum files first to leverage Docker cache
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application
# CGO_ENABLED=0 creates a statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o web-product-scraper .

# Use a minimal scratch image for the final stage
FROM scratch

# Copy ca-certificates from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary from builder
COPY --from=builder /app/web-product-scraper /web-product-scraper

# Copy the .env file if you want it baked in (NOT RECOMMENDED for secrets)
# OR rely on volume mounting / env vars at runtime.
# The user asked to "dockrize my backedned", usually implies just the image.
# We will NOT copy .env to the image to avoid leaking secrets.

# Expose the port
EXPOSE 8080

# Command to run the executable
ENTRYPOINT ["/web-product-scraper"]
