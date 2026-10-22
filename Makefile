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

# ==============================================================================
# Installation & Setup
# ==============================================================================

# Download Go module dependencies and generate go.sum
install:
	go mod tidy

# ==============================================================================
# Playground Targets
# ==============================================================================

# Launch local dev playground with web UI
playground:
	@echo "==============================================================================="
	@echo "| Starting your agent playground...                                           |"
	@echo "|                                                                             |"
	@echo "| Open: http://127.0.0.1:8501/ui/                                             |"
	@echo "| Try asking: 幫我找在 Cloud Summit 認識做 AI 的人                             |"
	@echo "==============================================================================="

	go run . web --port 8501 api -path_prefix / webui -api_server_address=

# ==============================================================================
# Local Development Commands
# ==============================================================================

# Launch local development server with API and A2A support (matches Agent Runtime / Cloud Run)
# API endpoints: /run_sse, /apps/...
# A2A endpoint: /a2a/invoke (JSON-RPC)
# Agent card: /.well-known/agent-card.json
local-backend:
	go run . web --port 8000 agentengine api -path_prefix / a2a appinfo

# Launch standalone LINE Webhook server
line-server:
	go run ./cmd/server

# ==============================================================================
# Backend Deployment Targets
# ==============================================================================

# Deploy the agent remotely to Vertex AI Agent Runtime
deploy:
	agents-cli deploy --yes

# Alias for 'make deploy' for backward compatibility
backend: deploy

# ==============================================================================
# Infrastructure Setup
# ==============================================================================

# Set up development environment resources using Terraform
setup-dev-env:
	PROJECT_ID=$$(gcloud config get-value project) && \
	(cd deployment/terraform/single-project && terraform init && terraform apply --var-file vars/env.tfvars --var project_id=$$PROJECT_ID --auto-approve)

# ==============================================================================
# Testing & Code Quality
# ==============================================================================

# Run unit and e2e tests
test:
	go test -v -tags=e2e ./...

# Run agents-cli eval suite (Quality Flywheel: generate + grade + Go trajectory suite)
eval:
	DRY_RUN=true agents-cli eval run --config eval/eval_config.yaml
	go test -v ./eval/...

# Run load tests (requires server running on port 8000)
# Server auto-loads .env on startup
# Usage: make load-test [DURATION=30s] [USERS=10] [RAMP=2]
load-test:
	_STAGING_URL=http://127.0.0.1:8000 go test -v -tags=load -timeout=5m ./e2e/load_test/... \
		-duration=$(or $(DURATION),30s) \
		-users=$(or $(USERS),10) \
		-ramp=$(or $(RAMP),2)

# Run code quality checks
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found, installing..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	}
	$$(go env GOPATH)/bin/golangci-lint run

# ==============================================================================
# Go-specific targets
# ==============================================================================

# Format Go code
fmt:
	go fmt ./...

# Build the binaries
build:
	go build -o bin/agent .
	go build -o bin/line-card-assistant ./cmd/server

# Clean build artifacts
clean:
	rm -rf bin/
