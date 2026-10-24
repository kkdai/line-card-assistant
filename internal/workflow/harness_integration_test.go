package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/harness"
	"github.com/kkdai/line-card-assistant/internal/store"
)

func TestHarnessEvolutionProgression(t *testing.T) {
	ctx := context.Background()

	// 1. Base Harness (k=0): Japanese trunk '(0)' is NOT stripped and colloquial TSMC search misses HybridSearch
	absBase, err := filepath.Abs("../../harness/base")
	if err != nil {
		t.Fatalf("absBase: %v", err)
	}
	t.Setenv(harness.EnvHarnessDir, absBase)

	engBase, err := NewEngine(config.Load(), store.NewMemoryStore(), NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine(base): %v", err)
	}
	resJPBase, err := engBase.StartIngest(ctx, "demo", []byte("ingest:jp_sony_sato"), nil)
	if err != nil {
		t.Fatalf("StartIngest(base): %v", err)
	}
	if resJPBase.DraftCard == nil || resJPBase.DraftCard.Phone != "+81 (0)3-6748-2111" {
		t.Fatalf("expected unnormalized JP phone in base harness, got %+v", resJPBase.DraftCard)
	}

	// 2. Evolved Sequential + LongTermMEMORY Harness (k=2):
	//    - Japanese card preserves bilingual name/title/company and normalizes '+81-3-6748-2111'
	//    - Colloquial TW semiconductor search hits TSMC directly in HybridSearch
	//    - FinTech executive email includes '[FinTech 合作提案]' compliance tag from LongTermMEMORY.md
	absK2, err := filepath.Abs("../../harness/evolved/k2_sequential_memory")
	if err != nil {
		t.Fatalf("absK2: %v", err)
	}
	t.Setenv(harness.EnvHarnessDir, absK2)

	engK2, err := NewEngine(config.Load(), store.NewMemoryStore(), NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine(k2): %v", err)
	}

	resJPK2, err := engK2.StartIngest(ctx, "demo", []byte("ingest:jp_sony_sato"), nil)
	if err != nil {
		t.Fatalf("StartIngest(k2): %v", err)
	}
	if resJPK2.DraftCard == nil || resJPK2.DraftCard.Phone != "+81-3-6748-2111" {
		t.Fatalf("expected normalized JP phone '+81-3-6748-2111' in k2 harness, got %+v", resJPK2.DraftCard)
	}
	if !strings.Contains(resJPK2.DraftCard.Name, "Kenichi Sato") {
		t.Fatalf("expected bilingual name in k2 harness, got %q", resJPK2.DraftCard.Name)
	}

	resSearchK2, err := engK2.RunSearch(ctx, "demo", "幫我找竹科半導體大廠做 CoWoS 先進封裝的人")
	if err != nil {
		t.Fatalf("RunSearch(k2): %v", err)
	}
	if len(resSearchK2.SearchResults) != 1 || !strings.Contains(resSearchK2.SearchResults[0].Company, "台積電") {
		t.Fatalf("expected 1 exact TSMC match in k2 harness, got %d matches", len(resSearchK2.SearchResults))
	}

	resEmailK2, err := engK2.RunEmailDraft(ctx, "demo", "card-seed-4")
	if err != nil {
		t.Fatalf("RunEmailDraft(k2): %v", err)
	}
	if !strings.Contains(resEmailK2.DraftEmailText, "主旨：[FinTech 合作提案]") {
		t.Fatalf("expected FinTech compliance subject tag from LongTermMEMORY.md, got:\n%s", resEmailK2.DraftEmailText)
	}
}
