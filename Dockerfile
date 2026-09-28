# ==========================================
# STAGE 1: Build binary
# ==========================================
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install ca-certificates and git
RUN apk add --no-cache ca-certificates git

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Compile static binary for Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /app/kido_app .

# ==========================================
# STAGE 2: Minimal runtime image
# ==========================================
FROM alpine:3.19

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

# Copy compiled binary and static assets
COPY --from=builder /app/kido_app /app/kido_app
COPY --from=builder /app/public /app/public
COPY --from=builder /app/schema.sql /app/schema.sql
COPY --from=builder /app/products_sample.json /app/products_sample.json

# Default environment variables
ENV PORT=8080
ENV GIN_MODE=release

EXPOSE 8080

ENTRYPOINT ["/app/kido_app"]
