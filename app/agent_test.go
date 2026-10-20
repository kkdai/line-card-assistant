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

package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kkdai/line-card-assistant/appinfo"
	"github.com/kkdai/line-card-assistant/internal/models"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestNewRootAgent(t *testing.T) {
	ctx := context.Background()
	defaultAgent, err := NewRootAgent(ctx)
	if err != nil {
		t.Fatalf("NewRootAgent() error = %v", err)
	}
	if defaultAgent.Name() != RootAgentName {
		t.Errorf("defaultAgent.Name() = %q, want %q", defaultAgent.Name(), RootAgentName)
	}

	rootAgent, err := NewRootAgentWithDeps(store.NewMemoryStore(), workflow.NewMockLLM())
	if err != nil {
		t.Fatalf("NewRootAgentWithDeps() error = %v", err)
	}
	if rootAgent.Name() != RootAgentName {
		t.Errorf("rootAgent.Name() = %q, want %q", rootAgent.Name(), RootAgentName)
	}

	info, err := appinfo.BuildAppInfo(agent.NewSingleLoader(rootAgent), rootAgent.Name())
	if err != nil {
		t.Fatalf("appinfo.BuildAppInfo() error = %v", err)
	}
	if info.RootAgentName != RootAgentName {
		t.Errorf("info.RootAgentName = %q, want %q", info.RootAgentName, RootAgentName)
	}
	if _, ok := info.Agents["CompanyEnrichAgent"]; !ok {
		t.Errorf("expected CompanyEnrichAgent in appinfo.Agents, got %v", info.Agents)
	}
}

func TestRootAgentDirectSearchExecution(t *testing.T) {
	ctx := context.Background()
	cardStore := store.NewMemoryStore()
	_ = cardStore.Save(ctx, &models.BusinessCard{
		ID:          "card-1",
		UserID:      "test-user",
		Name:        "王大明",
		Title:       "AI 技術副總",
		Company:     "台灣雲端智能股份有限公司",
		Phone:       "0912-345-678",
		Email:       "daming.wang@twcloudai.com",
		MetContext:  "Cloud Summit 2026",
		Tags:        []string{"AI", "Cloud"},
		CompanyInfo: "企業級生成式 AI 與混合雲解決方案領導廠商",
	})

	rootAgent, err := NewRootAgentWithDeps(cardStore, workflow.NewMockLLM())
	if err != nil {
		t.Fatalf("NewRootAgentWithDeps() error = %v", err)
	}

	sessionSvc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "app",
		Agent:             rootAgent,
		SessionService:    sessionSvc,
		ArtifactService:   artifact.InMemoryService(),
		MemoryService:     memory.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: "幫我找在 Cloud Summit 認識做 AI 的人"},
		},
	}

	var sawCarousel bool
	for ev, err := range r.Run(ctx, "test-user", "test-session-1", msg, agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("r.Run() error = %v", err)
		}
		if ev == nil {
			continue
		}
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p != nil && strings.Contains(p.Text, `"type":"carousel"`) || strings.Contains(p.Text, `"type": "carousel"`) {
					sawCarousel = true
				}
			}
		}
		if s, ok := ev.Actions.StateDelta[workflow.StateKeyFlexCarouselJSON].(string); ok {
			if strings.Contains(s, "carousel") {
				sawCarousel = true
			}
		}
	}
	if !sawCarousel {
		t.Errorf("expected carousel JSON output from root workflow agent on search query")
	}
}

func TestRootAgentDirectImageArtifactIngestAndHITL(t *testing.T) {
	ctx := context.Background()
	cardStore := store.NewMemoryStore()
	rootAgent, err := NewRootAgentWithDeps(cardStore, workflow.NewMockLLM())
	if err != nil {
		t.Fatalf("NewRootAgentWithDeps() error = %v", err)
	}

	sessionSvc := session.InMemoryService()
	artifactSvc := artifact.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "app",
		Agent:             rootAgent,
		SessionService:    sessionSvc,
		ArtifactService:   artifactSvc,
		MemoryService:     memory.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	// Simulate uploading an image directly from ADK WebUI / Agent Engine Playground / A2A
	imageData := []byte("fake-webui-uploaded-card-image")
	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			genai.NewPartFromBytes(imageData, "image/jpeg"),
		},
	}

	var interruptID string
	for ev, err := range r.Run(ctx, "webui-user", "webui-session-1", msg, agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("r.Run(image) error = %v", err)
		}
		if ev != nil && ev.RequestedInput != nil && ev.RequestedInput.InterruptID != "" {
			interruptID = ev.RequestedInput.InterruptID
		}
	}
	if interruptID == "" {
		t.Fatalf("expected rootAgent to pause at HITLConfirmNode and emit InterruptID")
	}

	// Verify RouterNode automatically persisted the uploaded image into ArtifactService
	loaded, err := artifactSvc.Load(ctx, &artifact.LoadRequest{
		AppName:   "app",
		UserID:    "webui-user",
		SessionID: "webui-session-1",
		FileName:  workflow.DefaultCardImageArtifactName,
	})
	if err != nil {
		t.Fatalf("expected uploaded image to be stored in ArtifactService: %v", err)
	}
	if loaded.Part == nil || loaded.Part.InlineData == nil || string(loaded.Part.InlineData.Data) != string(imageData) {
		t.Errorf("unexpected artifact data in ArtifactService")
	}
}
