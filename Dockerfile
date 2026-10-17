# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build stage
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache ca-certificates tzdata git

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY *.go ./
COPY app/ ./app/
COPY appinfo/ ./appinfo/
COPY sessions/ ./sessions/
COPY internal/ ./internal/
COPY cmd/ ./cmd/

# Build the Agent Runtime universal launcher binary (/agent) and standalone LINE server (/bin/line-card-assistant)
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /agent . && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/line-card-assistant ./cmd/server

# Runtime stage
FROM gcr.io/distroless/static-debian12

COPY --from=builder /agent /agent
COPY --from=builder /bin/line-card-assistant /bin/line-card-assistant

ARG COMMIT_SHA=""
ENV COMMIT_SHA=${COMMIT_SHA}

EXPOSE 8080

# The api launcher is mounted with `-path_prefix /` to serve the app at root, instead of under `/api`.
# appinfo already defaults to `/`, so it needs no explicit flag.
# Include extra agentengine launcher for compatibility with playground.
# For Agent Runtime port must be exactly 8080.
ENTRYPOINT ["/agent", "web", "--port", "8080", "agentengine", "api", "-path_prefix", "/", "a2a", "appinfo"]
