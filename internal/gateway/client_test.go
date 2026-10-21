package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kkdai/line-card-assistant/app"
	"github.com/kkdai/line-card-assistant/internal/gateway"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/server/adkrest"
	"google.golang.org/adk/v2/session"
)

type testAppLoader struct {
	appName string
	root    adkagent.Agent
}

func (l *testAppLoader) ListAgents() []string { return []string{l.appName} }
func (l *testAppLoader) LoadAgent(name string) (adkagent.Agent, error) {
	return l.root, nil
}
func (l *testAppLoader) RootAgent() adkagent.Agent { return l.root }

func newTestAgentRuntimeServer(t *testing.T) (*httptest.Server, store.CardStore, artifact.Service) {
	t.Helper()

	cardStore := store.NewMemoryStore()
	llm := workflow.NewMockLLM()

	rootAgent, err := app.NewRootAgentWithDeps(cardStore, llm)
	if err != nil {
		t.Fatalf("NewRootAgentWithDeps failed: %v", err)
	}

	sessionSvc := session.InMemoryService()
	artifactSvc := artifact.InMemoryService()

	restHandler, err := adkrest.NewServer(adkrest.ServerConfig{
		SessionService:  sessionSvc,
		ArtifactService: artifactSvc,
		MemoryService:   memory.InMemoryService(),
		AgentLoader:     &testAppLoader{appName: "app", root: rootAgent},
		SSEWriteTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("adkrest.NewServer failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", restHandler))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cardStore, artifactSvc
}

func TestGatewayClient_EndToEndDecoupledIngestHITLAndSearch(t *testing.T) {
	srv, cardStore, artifactSvc := newTestAgentRuntimeServer(t)
	ctx := context.Background()

	// Create Client 1 simulating Cloud Run LINE Webhook Gateway Instance #1
	client1, err := gateway.NewClient(
		ctx,
		srv.URL,
		false,
		gateway.WithHTTPClient(srv.Client()),
		gateway.WithAppName("app"),
	)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	userID := "U_GATEWAY_USER_01"
	client1.RecordUserMessage(userID, "今天在 2026 Google Cloud Summit Taipei 認識的新朋友")

	// Turn 1: StartIngest over HTTP to the remote Agent Runtime
	imgBytes := []byte("fake-jpeg-card-binary-payload")
	res1, err := client1.StartIngest(ctx, userID, imgBytes, nil)
	if err != nil {
		t.Fatalf("StartIngest failed: %v", err)
	}
	if !res1.WaitingHITL || res1.InterruptID == "" {
		t.Fatalf("expected WaitingHITL=true and non-empty InterruptID, got %+v", res1)
	}
	if !strings.Contains(res1.FlexPreviewJSON, "王大明") {
		t.Fatalf("expected FlexPreviewJSON to contain OCR extracted name '王大明', got %s", res1.FlexPreviewJSON)
	}

	// Verify that RouterNode on the remote Agent Runtime saved the uploaded image into ArtifactService
	listArt, err := artifactSvc.List(ctx, &artifact.ListRequest{
		AppName:   "app",
		UserID:    userID,
		SessionID: res1.SessionID,
	})
	if err != nil {
		t.Fatalf("artifactSvc.List failed: %v", err)
	}
	if len(listArt.FileNames) == 0 || listArt.FileNames[0] != workflow.DefaultCardImageArtifactName {
		t.Fatalf("expected remote ArtifactService to store %q, got %v", workflow.DefaultCardImageArtifactName, listArt.FileNames)
	}

	// Create Client 2 simulating a brand-new Cloud Run LINE Webhook Gateway Instance #2 (zero local cache)
	client2, err := gateway.NewClient(
		ctx,
		srv.URL,
		false,
		gateway.WithHTTPClient(srv.Client()),
		gateway.WithAppName("app"),
	)
	if err != nil {
		t.Fatalf("NewClient 2 failed: %v", err)
	}

	// Client 2 must discover the pending HITL session from the remote Agent Runtime's SessionService!
	if !client2.HasPendingHITL(userID) {
		t.Fatalf("expected stateless client2.HasPendingHITL(%q) to return true by querying remote sessions", userID)
	}

	// Turn 2: Natural language edit on Client 2
	res2, err := client2.ResumeHITL(ctx, userID, "職稱改為技術長")
	if err != nil {
		t.Fatalf("ResumeHITL (edit) on client2 failed: %v", err)
	}
	if !res2.WaitingHITL {
		t.Fatalf("expected workflow to pause again at HITLConfirmNode after edit")
	}
	if res2.DraftCard == nil || res2.DraftCard.Title != "技術長" {
		t.Fatalf("expected updated DraftCard.Title='技術長', got %+v", res2.DraftCard)
	}

	// Turn 3: Confirm & Save on Client 1
	res3, err := client1.ResumeHITL(ctx, userID, "action=confirm_save")
	if err != nil {
		t.Fatalf("ResumeHITL (confirm) failed: %v", err)
	}
	if res3.WaitingHITL {
		t.Fatalf("expected WaitingHITL=false after confirm_save")
	}
	if res3.SavedCardID == "" {
		t.Fatalf("expected non-empty SavedCardID after confirm_save")
	}
	if !strings.Contains(res3.ReplyText, "已成功儲存") {
		t.Fatalf("expected confirmation reply text, got %q", res3.ReplyText)
	}

	// Verify the card was persisted in the remote Agent Runtime's CardStore
	savedCard, err := cardStore.GetByID(ctx, res3.SavedCardID)
	if err != nil {
		t.Fatalf("expected saved card in remote CardStore: %v", err)
	}
	if savedCard.Title != "技術長" {
		t.Fatalf("expected saved card title '技術長', got %q", savedCard.Title)
	}

	// Turn 4: RunSearch over HTTP
	searchRes, err := client2.RunSearch(ctx, userID, "幫我找在 Cloud Summit 認識做 AI 的人")
	if err != nil {
		t.Fatalf("RunSearch failed: %v", err)
	}
	if searchRes.FlexCarouselJSON == "" {
		t.Fatalf("expected non-empty FlexCarouselJSON from remote RunSearch")
	}

	// Turn 5: RunEmailDraft over HTTP
	emailRes, err := client2.RunEmailDraft(ctx, userID, res3.SavedCardID)
	if err != nil {
		t.Fatalf("RunEmailDraft failed: %v", err)
	}
	if !strings.Contains(emailRes.DraftEmailText, "主旨") {
		t.Fatalf("expected follow-up email draft with subject line, got %q", emailRes.DraftEmailText)
	}
}
