package workflow

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/store"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"
)

func TestCardIngestAndHITLResume(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()
	cfg := config.Load()
	engine, err := NewEngine(cfg, memStore, NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	userID := "test-user-ingest"
	engine.RecordUserMessage(userID, "2026 AI DevFest Taipei")

	// Step 1: Upload business card image -> should run VisionOCR + 3 parallel nodes + BuildPreview and pause at HITLConfirmNode
	turn1, err := engine.StartIngest(ctx, userID, []byte("fake-jpeg-bytes"), nil)
	if err != nil {
		t.Fatalf("StartIngest failed: %v", err)
	}
	if !turn1.WaitingHITL {
		t.Fatalf("expected workflow to pause at HITLConfirmNode, got WaitingHITL=false")
	}
	if turn1.InterruptID == "" {
		t.Fatalf("expected non-empty InterruptID when paused at HITLConfirmNode")
	}
	if !engine.HasPendingHITL(userID) {
		t.Fatalf("expected engine.HasPendingHITL(%q) == true", userID)
	}
	if turn1.DraftCard == nil {
		t.Fatalf("expected DraftCard in TurnResult")
	}
	if turn1.DraftCard.Phone != "+886-912-345-678" {
		t.Errorf("expected normalized phone +886-912-345-678, got %q", turn1.DraftCard.Phone)
	}
	if turn1.DraftCard.CompanyInfo == "" {
		t.Errorf("expected enriched CompanyInfo to be populated")
	}
	if len(turn1.DraftCard.Tags) == 0 {
		t.Errorf("expected enriched Tags to be populated")
	}
	if turn1.DraftCard.MetContext != "2026 AI DevFest Taipei" {
		t.Errorf("expected MetContext %q, got %q", "2026 AI DevFest Taipei", turn1.DraftCard.MetContext)
	}
	if turn1.FlexPreviewJSON == "" {
		t.Fatalf("expected FlexPreviewJSON to be populated")
	}
	if _, _, err := linebot.BuildPreviewFlexMessage(turn1.DraftCard, false, ""); err != nil {
		t.Fatalf("BuildPreviewFlexMessage validation failed: %v", err)
	}

	// Verify parallel nodes and preview nodes emitted events
	seenNodes := make(map[string]bool)
	for _, ev := range turn1.Events {
		if ev != nil && ev.NodeInfo != nil {
			for _, name := range []string{
				NodeRouter, NodeVisionOCR, NodeCleanAndDedup,
				NodeCompanyEnrich, NodeContextMatch, NodeBuildPreview, NodeHITLConfirm,
			} {
				if strings.Contains(ev.NodeInfo.Path, name) {
					seenNodes[name] = true
				}
			}
		}
	}
	for _, expected := range []string{
		NodeRouter, NodeVisionOCR, NodeCleanAndDedup,
		NodeCompanyEnrich, NodeContextMatch, NodeBuildPreview, NodeHITLConfirm,
	} {
		if !seenNodes[expected] {
			t.Errorf("expected node %q to have executed in Turn 1, seenNodes=%v", expected, seenNodes)
		}
	}

	// Step 2: Resume with "action=confirm_save" -> should route to SaveCardNode and persist to MemoryStore
	turn2, err := engine.ResumeHITL(ctx, userID, "action=confirm_save")
	if err != nil {
		t.Fatalf("ResumeHITL(confirm_save) failed: %v", err)
	}
	if turn2.WaitingHITL {
		t.Fatalf("expected workflow to complete after confirm_save, got WaitingHITL=true")
	}
	if engine.HasPendingHITL(userID) {
		t.Fatalf("expected HasPendingHITL(%q) == false after confirm_save", userID)
	}
	if turn2.SavedCardID == "" {
		t.Fatalf("expected SavedCardID to be populated after SaveCardNode")
	}

	savedCard, err := memStore.GetByID(ctx, turn2.SavedCardID)
	if err != nil {
		t.Fatalf("GetByID(%q) failed: %v", turn2.SavedCardID, err)
	}
	if savedCard.Name != "王大明 (Ming Wang)" {
		t.Errorf("unexpected saved card Name: %q", savedCard.Name)
	}
	if savedCard.Phone != "+886-912-345-678" {
		t.Errorf("unexpected saved card Phone: %q", savedCard.Phone)
	}
}

func TestCardIngestEditLoop(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()
	cfg := config.Load()
	engine, err := NewEngine(cfg, memStore, NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	userID := "test-user-edit-loop"

	// Turn 1: Upload image -> pauses at HITLConfirmNode
	turn1, err := engine.StartIngest(ctx, userID, []byte("fake-jpeg-bytes"), []string{"2026 Cloud Summit Taipei"})
	if err != nil {
		t.Fatalf("StartIngest failed: %v", err)
	}
	if !turn1.WaitingHITL {
		t.Fatalf("expected WaitingHITL=true on Turn 1")
	}
	if turn1.DraftCard == nil || turn1.DraftCard.Title != "資深技術經理" {
		t.Fatalf("expected initial Title '資深技術經理', got %+v", turn1.DraftCard)
	}

	// Turn 2: User sends natural language edit "職稱改為處長" -> ApplyEditNode -> BuildPreviewNode -> pauses again at HITLConfirmNode
	turn2, err := engine.ResumeHITL(ctx, userID, "職稱改為處長")
	if err != nil {
		t.Fatalf("ResumeHITL(edit) failed: %v", err)
	}
	if !turn2.WaitingHITL {
		t.Fatalf("expected workflow to pause again at HITLConfirmNode after edit, got WaitingHITL=false")
	}
	if turn2.DraftCard == nil {
		t.Fatalf("expected updated DraftCard on Turn 2")
	}
	if turn2.DraftCard.Title != "處長" {
		t.Fatalf("expected updated DraftCard.Title == '處長', got %q", turn2.DraftCard.Title)
	}
	if !strings.Contains(turn2.FlexPreviewJSON, "處長") {
		t.Errorf("expected re-rendered FlexPreviewJSON to contain '處長'")
	}

	// Turn 3: User confirms save -> SaveCardNode persists card with Title == "處長"
	turn3, err := engine.ResumeHITL(ctx, userID, "action=confirm_save")
	if err != nil {
		t.Fatalf("ResumeHITL(confirm_save) failed: %v", err)
	}
	if turn3.WaitingHITL {
		t.Fatalf("expected WaitingHITL=false after final confirm")
	}
	if turn3.SavedCardID == "" {
		t.Fatalf("expected non-empty SavedCardID")
	}

	saved, err := memStore.GetByID(ctx, turn3.SavedCardID)
	if err != nil {
		t.Fatalf("GetByID(%q) failed: %v", turn3.SavedCardID, err)
	}
	if saved.Title != "處長" {
		t.Errorf("expected saved card Title == '處長', got %q", saved.Title)
	}
}

func TestHybridSearchFlow(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()
	cfg := config.Load()
	engine, err := NewEngine(cfg, memStore, NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	// Query matching pre-seeded Cloud Summit + AI business cards
	res, err := engine.RunSearch(ctx, store.DefaultDemoUserID, "幫我找上週在 Cloud Summit 認識做 AI 的人")
	if err != nil {
		t.Fatalf("RunSearch failed: %v", err)
	}
	if len(res.SearchResults) == 0 {
		t.Fatalf("expected at least 1 search result for Cloud Summit + AI")
	}
	if res.FlexCarouselJSON == "" {
		t.Fatalf("expected FlexCarouselJSON to be populated")
	}
	if _, _, err := linebot.BuildSearchCarouselFlexMessage(res.SearchResults); err != nil {
		t.Fatalf("BuildSearchCarouselFlexMessage failed: %v", err)
	}
}

func TestEmailDraftFlow(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()
	cfg := config.Load()
	engine, err := NewEngine(cfg, memStore, NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	res, err := engine.RunEmailDraft(ctx, store.DefaultDemoUserID, "card-seed-1")
	if err != nil {
		t.Fatalf("RunEmailDraft failed: %v", err)
	}
	if strings.TrimSpace(res.DraftEmailText) == "" {
		t.Fatalf("expected non-empty DraftEmailText")
	}
}

func TestCrossInstanceHITLResumeAndArtifactStorage(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()
	cfg := config.Load()

	// Shared SessionService and ArtifactService simulating Google Cloud Agent Platform
	// (session/vertexai + artifact/gcsartifact) across two independent stateless container instances.
	sharedSessionSvc := session.InMemoryService()
	sharedArtifactSvc := artifact.InMemoryService()

	instanceA, err := NewEngine(
		cfg,
		memStore,
		NewMockLLM(),
		WithSessionService(sharedSessionSvc),
		WithArtifactService(sharedArtifactSvc),
	)
	if err != nil {
		t.Fatalf("NewEngine(instanceA) failed: %v", err)
	}

	instanceB, err := NewEngine(
		cfg,
		memStore,
		NewMockLLM(),
		WithSessionService(sharedSessionSvc),
		WithArtifactService(sharedArtifactSvc),
	)
	if err != nil {
		t.Fatalf("NewEngine(instanceB) failed: %v", err)
	}

	userID := "cross-instance-user"
	cardPhoto := []byte("fake-high-res-card-jpeg-payload")

	// Turn 1 runs on Instance A: uploads card photo -> saves to ArtifactService -> pauses at HITLConfirmNode
	turn1, err := instanceA.StartIngest(ctx, userID, cardPhoto, []string{"2026 Google Cloud Next Tokyo"})
	if err != nil {
		t.Fatalf("instanceA.StartIngest failed: %v", err)
	}
	if !turn1.WaitingHITL {
		t.Fatalf("expected turn1.WaitingHITL == true on instanceA")
	}

	// Verify the binary image was saved in ArtifactService and NOT bloated into Session.State[StateKeyImageBytes]
	loadedArt, err := sharedArtifactSvc.Load(ctx, &artifact.LoadRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: turn1.SessionID,
		FileName:  DefaultCardImageArtifactName,
	})
	if err != nil {
		t.Fatalf("expected card photo in ArtifactService, Load failed: %v", err)
	}
	if loadedArt.Part == nil || loadedArt.Part.InlineData == nil || !bytes.Equal(loadedArt.Part.InlineData.Data, cardPhoto) {
		t.Fatalf("artifact bytes mismatch: got %v, want %v", loadedArt.Part, cardPhoto)
	}

	sessResp, err := sharedSessionSvc.Get(ctx, &session.GetRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: turn1.SessionID,
	})
	if err != nil {
		t.Fatalf("sharedSessionSvc.Get failed: %v", err)
	}
	if rawImg, err := sessResp.Session.State().Get(StateKeyImageBytes); err == nil && rawImg != nil {
		t.Errorf("expected StateKeyImageBytes to be omitted from Session.State when ArtifactService is active, got %T", rawImg)
	}
	if artName := readStringFromSession(sessResp.Session, StateKeyCardImageArtifact); artName != DefaultCardImageArtifactName {
		t.Errorf("expected StateKeyCardImageArtifact=%q in Session.State, got %q", DefaultCardImageArtifactName, artName)
	}

	// Turn 2 lands on Instance B (which has an empty local pendingSession map!):
	// Instance B must discover the paused session from sharedSessionSvc and resume the edit loop.
	if !instanceB.HasPendingHITL(userID) {
		t.Fatalf("expected stateless instanceB.HasPendingHITL(%q) == true via shared SessionService", userID)
	}

	turn2, err := instanceB.ResumeHITL(ctx, userID, "職稱改為技術長")
	if err != nil {
		t.Fatalf("instanceB.ResumeHITL(edit) failed: %v", err)
	}
	if !turn2.WaitingHITL {
		t.Fatalf("expected turn2.WaitingHITL == true after edit on instanceB")
	}
	if turn2.DraftCard == nil || turn2.DraftCard.Title != "技術長" {
		t.Fatalf("expected updated Title '技術長' on instanceB, got %+v", turn2.DraftCard)
	}

	// Turn 3 lands back on Instance A (or another fresh Instance C) to confirm and save
	instanceC, err := NewEngine(
		cfg,
		memStore,
		NewMockLLM(),
		WithSessionService(sharedSessionSvc),
		WithArtifactService(sharedArtifactSvc),
	)
	if err != nil {
		t.Fatalf("NewEngine(instanceC) failed: %v", err)
	}

	turn3, err := instanceC.ResumeHITL(ctx, userID, "action=confirm_save")
	if err != nil {
		t.Fatalf("instanceC.ResumeHITL(confirm_save) failed: %v", err)
	}
	if turn3.WaitingHITL {
		t.Fatalf("expected turn3.WaitingHITL == false after confirm_save on instanceC")
	}
	if turn3.SavedCardID == "" {
		t.Fatalf("expected SavedCardID after confirm_save on instanceC")
	}

	saved, err := memStore.GetByID(ctx, turn3.SavedCardID)
	if err != nil {
		t.Fatalf("memStore.GetByID failed: %v", err)
	}
	if saved.Title != "技術長" {
		t.Errorf("expected saved card Title == '技術長', got %q", saved.Title)
	}
}
