// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package sessions builds the process-wide ADK session service for the chosen backend.
package sessions

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/agentplatform"
	"cloud.google.com/go/agentplatform/types"
	"google.golang.org/api/option"
	"google.golang.org/genai"
	"gorm.io/driver/postgres"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"google.golang.org/adk/v2/session/vertexai"
)

// sessionType is the backend selected at scaffold time.
const sessionType = "in_memory"

// defaultAgentRuntimeName is the display name used to find or create the managed
// Agent Runtime that backs agent_platform_sessions.
const defaultAgentRuntimeName = "line-card-assistant"

// defaultAgentRuntimeLocation is the region Agent Runtime sessions default to
// when the agent itself is deployed elsewhere.
const defaultAgentRuntimeLocation = "us-central1"

// dbConnectTimeout bounds how long cloudSQLSessionService waits for the database
// to become reachable before giving up.
const dbConnectTimeout = 60 * time.Second

// dbConnectRetryInterval is the delay between database connection attempts while
// waiting for the database to become reachable.
const dbConnectRetryInterval = 2 * time.Second

// NewService returns the ADK session service for the selected backend.
func NewService(ctx context.Context) (session.Service, error) {
	backend := envOrDefault("SESSION_TYPE", sessionType)
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "cloud_sql":
		return cloudSQLSessionService()
	case "agent_platform_sessions", "agent_engine", "vertexai":
		return agentPlatformSessions(ctx)
	default:
		engineID := os.Getenv("GOOGLE_CLOUD_AGENT_ENGINE_ID")
		if engineID == "" {
			return session.InMemoryService(), nil
		}
		loc := agentRuntimeLocation()
		// vertexService parses a full resource name; GOOGLE_CLOUD_AGENT_ENGINE_ID
		// is a bare id, so build the full name from it.
		name := fmt.Sprintf(
			"projects/%s/locations/%s/reasoningEngines/%s",
			os.Getenv("GOOGLE_CLOUD_PROJECT"), loc, engineID,
		)
		return vertexService(ctx, name)
	}
}

// cloudSQLSessionService backs sessions with Cloud SQL when the connection is configured,
// otherwise in-memory.
func cloudSQLSessionService() (session.Service, error) {
	dbUser := envOrDefault("DB_USER", "postgres")
	dbName := envOrDefault("DB_NAME", "postgres")
	dbPass := os.Getenv("DB_PASS")
	instanceConnectionName := os.Getenv("INSTANCE_CONNECTION_NAME")

	if instanceConnectionName == "" || dbPass == "" {
		return session.InMemoryService(), nil
	}

	dsn := fmt.Sprintf(
		"host=/cloudsql/%s user=%s password=%s dbname=%s sslmode=disable",
		instanceConnectionName,
		dbUser,
		dbPass,
		dbName,
	)

	deadline := time.Now().Add(dbConnectTimeout)
	for attempt := 1; ; attempt++ {
		svc, err := openCloudSQL(dsn)
		if err == nil {
			return svc, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect to Cloud SQL within %s: %w", dbConnectTimeout, err)
		}
		log.Printf("Cloud SQL not ready (attempt %d): %v; retrying in %s", attempt, err, dbConnectRetryInterval)
		time.Sleep(dbConnectRetryInterval)
	}
}

// openCloudSQL opens the database session service and applies migrations.
func openCloudSQL(dsn string) (session.Service, error) {
	svc, err := database.NewSessionService(postgres.Open(dsn))
	if err != nil {
		return nil, fmt.Errorf("create Cloud SQL session service: %w", err)
	}
	if err := database.AutoMigrate(svc); err != nil {
		return nil, fmt.Errorf("migrate Cloud SQL session schema: %w", err)
	}
	return svc, nil
}

// agentPlatformSessions backs sessions with a managed Agent Runtime,
// finding or creating one by display name.
func agentPlatformSessions(ctx context.Context) (session.Service, error) {
	forceInMemory := os.Getenv("USE_IN_MEMORY_SESSION")
	if forceInMemory == "true" || forceInMemory == "1" || forceInMemory == "yes" {
		return session.InMemoryService(), nil
	}

	name := envOrDefault("AGENT_ENGINE_SESSION_NAME", defaultAgentRuntimeName)
	loc := agentRuntimeLocation()

	engineName, err := findOrCreateAgentRuntime(
		ctx, os.Getenv("GOOGLE_CLOUD_PROJECT"), loc, name,
	)
	if err != nil {
		return nil, err
	}

	return vertexService(ctx, engineName)
}

// vertexService builds an ADK vertexai session service against an existing Agent Runtime.
func vertexService(ctx context.Context, engineName string) (session.Service, error) {
	parts := strings.Split(engineName, "/")
	project := parts[1]
	location := parts[3]
	id := parts[5]

	svc, err := vertexai.NewSessionService(
		ctx,
		vertexai.VertexAIServiceConfig{
			ProjectID:       project,
			Location:        location,
			ReasoningEngine: id,
		},
		option.WithEndpoint(aiplatformEndpoint(location)),
	)
	if err != nil {
		return nil, fmt.Errorf("create Agent Runtime session service: %w", err)
	}
	return svc, nil
}

// findOrCreateAgentRuntime returns the resource name of the Agent Runtime with
// the given display name, creating a session-only Agent Runtime if none exists.
func findOrCreateAgentRuntime(ctx context.Context, project, location, displayName string) (string, error) {
	client, err := agentplatform.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  project,
		Location: location,
	})
	if err != nil {
		return "", fmt.Errorf("create Gemini platform client: %w", err)
	}

	resp, err := client.AgentEngines.List(ctx, &types.ListAgentEngineConfig{
		Filter: fmt.Sprintf("display_name=%q", displayName),
	})
	if err != nil {
		return "", fmt.Errorf("list Agent Runtimes: %w", err)
	}
	if len(resp.ReasoningEngines) > 0 {
		return resp.ReasoningEngines[0].Name, nil
	}

	op, err := client.AgentEngines.Create(ctx, &types.CreateAgentEngineConfig{
		DisplayName: displayName,
	})
	if err != nil {
		return "", fmt.Errorf("create Agent Runtime: %w", err)
	}
	for !op.Done {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if op, err = client.AgentEngines.GetAgentOperation(ctx, op.Name, nil); err != nil {
			return "", fmt.Errorf("await Agent Runtime creation: %w", err)
		}
	}
	if op.Response == nil {
		return "", fmt.Errorf("no engine returned from Agent Runtime creation")
	}
	return op.Response.Name, nil
}

// agentRuntimeLocation resolves the region for Agent Runtime operations.
//
// Agent Runtime is a regional resource and it doesn't support global location.
func agentRuntimeLocation() string {
	if loc := os.Getenv("GOOGLE_CLOUD_AGENT_ENGINE_LOCATION"); loc != "" {
		return loc
	}
	if loc := os.Getenv("GOOGLE_CLOUD_LOCATION"); loc != "" && loc != "global" {
		return loc
	}
	return defaultAgentRuntimeLocation
}

// aiplatformEndpoint returns the regional aiplatform endpoint for a location.
func aiplatformEndpoint(location string) string {
	return fmt.Sprintf("%s-aiplatform.googleapis.com:443", location)
}

// envOrDefault returns the value of the environment variable named key, or
// fallback when it is unset or empty.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
