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

// Package app contains the root ADK 2.0 Graph Workflow agent implementation for line-card-assistant.
package app

import (
	"context"
	"log/slog"

	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
)

// RootAgentName must stay in sync with agents-cli-manifest.yaml (`root_agent_name`).
// Telemetry reports this as gen_ai.agent.name, and agents-cli uses it to identify the root agent.
const RootAgentName = "line_card_assistant"

// NewRootAgent creates and returns the root ADK 2.0 Graph Workflow agent (`line_card_assistant`)
// wired with the configured Gemini / Vertex AI model (or MockLLM fallback in local/dry-run environments)
// and BusinessCard store.
func NewRootAgent(ctx context.Context) (agent.Agent, error) {
	cfg := config.Load()
	cardStore := store.NewMemoryStore()

	llm, err := workflow.NewLLMModel(ctx, cfg)
	if err != nil {
		slog.Warn("falling back to MockLLM due to LLM initialization error", "err", err)
		llm = workflow.NewMockLLM()
	}

	return NewRootAgentWithDeps(cardStore, llm)
}

// NewRootAgentWithDeps builds the root ADK 2.0 Graph Workflow agent with explicit store and LLM dependencies.
func NewRootAgentWithDeps(cardStore store.CardStore, llm model.LLM) (agent.Agent, error) {
	if cardStore == nil {
		cardStore = store.NewMemoryStore()
	}
	if llm == nil {
		llm = workflow.NewMockLLM()
	}

	edges, err := workflow.BuildEdges(cardStore, llm)
	if err != nil {
		return nil, err
	}

	var subAgents []agent.Agent
	if enrichAgent, err := workflow.NewCompanyEnrichAgent(llm); err == nil && enrichAgent != nil {
		subAgents = append(subAgents, enrichAgent)
	}

	return workflowagent.New(workflowagent.Config{
		Name:        RootAgentName,
		Description: "LINE AI Business Card Assistant workflow orchestrating Vision OCR, parallel company & context enrichment, HITL confirmation loop, hybrid search, and follow-up email drafting.",
		Edges:       edges,
		SubAgents:   subAgents,
	})
}
