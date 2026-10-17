# syntax=docker/dockerfile:1

# Stage 1: Build binary
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache ca-certificates tzdata git

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /bin/line-card-assistant \
    ./cmd/server

# Stage 2: Minimal Cloud Run runtime image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app
COPY --from=builder /bin/line-card-assistant /app/line-card-assistant

USER appuser
ENV PORT=8080
EXPOSE 8080

ENTRYPOINT ["/app/line-card-assistant"]
