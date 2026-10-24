package workflow

import (
	"context"
	"testing"

	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func setupTestSpanExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return exporter
}

func TestWorkflowOpenTelemetrySpans_IngestAndHITL(t *testing.T) {
	exporter := setupTestSpanExporter(t)
	ctx := context.Background()

	memStore := store.NewMemoryStore()
	cfg := config.Load()
	engine, err := NewEngine(cfg, memStore, NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	userID := "otel-user-1"
	turn1, err := engine.StartIngest(ctx, userID, []byte("fake-card-image"), nil)
	if err != nil {
		t.Fatalf("StartIngest: %v", err)
	}
	if !turn1.WaitingHITL || turn1.InterruptID == "" {
		t.Fatalf("expected HITL pause, got WaitingHITL=%v InterruptID=%q", turn1.WaitingHITL, turn1.InterruptID)
	}

	turn2, err := engine.ResumeHITL(ctx, userID, "職稱改為技術副總")
	if err != nil {
		t.Fatalf("ResumeHITL edit: %v", err)
	}
	if !turn2.WaitingHITL {
		t.Fatalf("expected HITL pause after edit loop")
	}

	turn3, err := engine.ResumeHITL(ctx, userID, "action=confirm_save")
	if err != nil {
		t.Fatalf("ResumeHITL confirm: %v", err)
	}
	if turn3.SavedCardID == "" {
		t.Fatal("expected SavedCardID after confirm_save")
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected OpenTelemetry spans to be recorded, got 0")
	}

	seenNodes := make(map[string]bool)
	for _, s := range spans {
		for _, attr := range s.Attributes {
			if attr.Key == AttrWorkflowNode {
				seenNodes[attr.Value.AsString()] = true
			}
		}
	}

	expectedNodes := []string{
		NodeRouter,
		NodeVisionOCR,
		NodeCleanAndDedup,
		NodeCompanyEnrich,
		NodeContextMatch,
		NodeBuildPreview,
		NodeHITLConfirm,
		NodeApplyEdit,
		NodeSaveCard,
	}
	for _, want := range expectedNodes {
		if !seenNodes[want] {
			t.Errorf("missing expected OpenTelemetry node span for %q (seen=%v)", want, seenNodes)
		}
	}
}

func TestWorkflowOpenTelemetrySpans_SearchFallback(t *testing.T) {
	exporter := setupTestSpanExporter(t)
	ctx := context.Background()

	memStore := store.NewMemoryStore()
	cfg := config.Load()
	engine, err := NewEngine(cfg, memStore, NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	_, err = engine.RunSearch(ctx, "otel-search-user", "幫我找上週認識做 AI 的人")
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	spans := exporter.GetSpans()
	seenNodes := make(map[string]map[attribute.Key]attribute.Value)
	for _, s := range spans {
		attrMap := make(map[attribute.Key]attribute.Value)
		for _, a := range s.Attributes {
			attrMap[a.Key] = a.Value
		}
		if nodeVal, ok := attrMap[AttrWorkflowNode]; ok {
			seenNodes[nodeVal.AsString()] = attrMap
		}
	}

	for _, want := range []string{NodeRouter, NodeQueryIntent, NodeHybridSearch, NodeBroadSearch, NodeFormatCarousel} {
		if _, ok := seenNodes[want]; !ok {
			t.Errorf("missing expected search node span %q", want)
		}
	}
	if route := seenNodes[NodeHybridSearch][AttrWorkflowRoute].AsString(); route != RouteEmpty {
		t.Errorf("expected HybridSearchNode span route=%q on empty store, got %q", RouteEmpty, route)
	}
}
