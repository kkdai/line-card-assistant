package eval_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkdai/line-card-assistant/app"
	"github.com/kkdai/line-card-assistant/appinfo"
	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
	"google.golang.org/adk/v2/agent"
)

type evalDatasetFile struct {
	EvalCases []struct {
		EvalCaseID       string `json:"eval_case_id"`
		Prompt           struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"prompt"`
		CustomProperties map[string]any `json:"custom_Properties"`
	} `json:"eval_cases"`
}

func TestEvalDatasetAndAppInfoIntrospection(t *testing.T) {
	t.Setenv("DRY_RUN", "true")

	raw, err := os.ReadFile(filepath.Join("datasets", "basic-dataset.json"))
	if err != nil {
		t.Fatalf("read eval/datasets/basic-dataset.json: %v", err)
	}

	var ds evalDatasetFile
	if err := json.Unmarshal(raw, &ds); err != nil {
		t.Fatalf("unmarshal basic-dataset.json: %v", err)
	}
	if len(ds.EvalCases) < 6 {
		t.Fatalf("expected at least 6 eval cases in basic-dataset.json, got %d", len(ds.EvalCases))
	}

	// Verify /apps/app/app-info reflection metadata required by `agents-cli eval generate`
	ctx := context.Background()
	rootAgent, err := app.NewRootAgent(ctx)
	if err != nil {
		t.Fatalf("NewRootAgent failed: %v", err)
	}
	info, err := appinfo.BuildAppInfo(agent.NewSingleLoader(rootAgent), "line_card_assistant")
	if err != nil {
		t.Fatalf("BuildAppInfo failed: %v", err)
	}
	if info.RootAgentName != "line_card_assistant" {
		t.Fatalf("expected rootAgentName=line_card_assistant, got %q", info.RootAgentName)
	}
	enrichInfo, ok := info.Agents["CompanyEnrichAgent"]
	if !ok {
		t.Fatalf("expected CompanyEnrichAgent in app-info agents map, got %v", info.Agents)
	}
	if !strings.Contains(enrichInfo.Instruction, "專業商業分析助理") {
		t.Fatalf("expected CompanyEnrichAgent instruction to describe 專業商業分析助理, got %q", enrichInfo.Instruction)
	}
}

func TestPhase4WorkflowTrajectorySuite(t *testing.T) {
	t.Setenv("DRY_RUN", "true")
	ctx := context.Background()
	cfg := config.Load()
	cardStore := store.NewMemoryStore()
	engine, err := workflow.NewEngine(cfg, cardStore, workflow.NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	t.Run("Case1_IngestFanOutFanInOCR", func(t *testing.T) {
		res, err := engine.StartIngest(ctx, "eval-user-1", []byte("fake-card-image"), []string{"2026 Cloud Summit Taipei"})
		if err != nil {
			t.Fatalf("StartIngest failed: %v", err)
		}
		if !res.WaitingHITL || res.InterruptID == "" {
			t.Fatalf("expected HITL pause, got WaitingHITL=%v InterruptID=%q", res.WaitingHITL, res.InterruptID)
		}
		if res.DraftCard == nil {
			t.Fatalf("expected non-nil DraftCard")
		}
		if res.DraftCard.Phone != "+886-912-345-678" {
			t.Fatalf("expected E.164 phone +886-912-345-678, got %q", res.DraftCard.Phone)
		}
		if len(res.DraftCard.Tags) < 3 || res.DraftCard.CompanyInfo == "" {
			t.Fatalf("expected enriched company info and >=3 tags, got info=%q tags=%v", res.DraftCard.CompanyInfo, res.DraftCard.Tags)
		}
		if !strings.Contains(res.FlexPreviewJSON, `"type":"bubble"`) {
			t.Fatalf("expected valid Flex Bubble JSON")
		}
	})

	t.Run("Case2_DuplicatePromotionDetection", func(t *testing.T) {
		res, err := engine.StartIngest(ctx, "demo", []byte("promotion_david_chen_appier"), []string{"2026 Cloud Summit Taipei"})
		if err != nil {
			t.Fatalf("StartIngest duplicate promotion failed: %v", err)
		}
		if res.DraftCard == nil || !strings.Contains(res.DraftCard.Title, "CTO") {
			t.Fatalf("expected promoted title CTO, got %+v", res.DraftCard)
		}
		if !strings.Contains(res.FlexPreviewJSON, "發現重複名片") || !strings.Contains(res.FlexPreviewJSON, "VP of Engineering") {
			t.Fatalf("expected duplicate warning and old title VP of Engineering in Flex JSON, got: %s", res.FlexPreviewJSON)
		}
	})

	t.Run("Case3_MultiTurnHITLEditAndConfirmLoop", func(t *testing.T) {
		userID := "eval-hitl-user"
		// Turn 1: Upload business card -> pauses at HITLConfirmNode
		turn1, err := engine.StartIngest(ctx, userID, []byte("fake-card-image"), []string{"2026 Cloud Summit Taipei"})
		if err != nil {
			t.Fatalf("Turn 1 StartIngest failed: %v", err)
		}
		if !turn1.WaitingHITL {
			t.Fatalf("expected Turn 1 to pause at HITLConfirmNode")
		}

		// Turn 2: Natural language edit -> ApplyEditNode -> BuildPreviewNode -> pauses at HITLConfirmNode
		turn2, err := engine.ResumeHITL(ctx, userID, "職稱改為技術副總，場合改為 COMPUTEX 2026 AI 論壇")
		if err != nil {
			t.Fatalf("Turn 2 ResumeHITL edit failed: %v", err)
		}
		if !turn2.WaitingHITL {
			t.Fatalf("expected Turn 2 to pause again at HITLConfirmNode after rebuilding preview")
		}
		if turn2.DraftCard == nil || turn2.DraftCard.Title != "技術副總" || turn2.DraftCard.MetContext != "COMPUTEX 2026 AI 論壇" {
			t.Fatalf("expected updated title & met_context on Turn 2, got %+v", turn2.DraftCard)
		}

		// Turn 3: Confirm save -> SaveCardNode persists card & generates icebreaker email
		turn3, err := engine.ResumeHITL(ctx, userID, "action=confirm_save")
		if err != nil {
			t.Fatalf("Turn 3 ResumeHITL confirm failed: %v", err)
		}
		if turn3.WaitingHITL {
			t.Fatalf("expected Turn 3 to complete workflow without HITL pause")
		}
		if turn3.SavedCardID == "" || !strings.Contains(turn3.ReplyText, "✅ 名片已成功儲存") || !strings.Contains(turn3.ReplyText, "破冰跟進信草稿") {
			t.Fatalf("unexpected Turn 3 completion output: SavedCardID=%q ReplyText=%q", turn3.SavedCardID, turn3.ReplyText)
		}
	})

	t.Run("Case4_SearchHybridAndBroadFallbackAndEmailDraft", func(t *testing.T) {
		// 4a: Hybrid Search
		hybridRes, err := engine.RunSearch(ctx, "demo", "幫我找上週在 Cloud Summit 認識做 AI 的人")
		if err != nil {
			t.Fatalf("RunSearch hybrid failed: %v", err)
		}
		if len(hybridRes.SearchResults) < 2 || !strings.Contains(hybridRes.FlexCarouselJSON, "林雅婷") {
			t.Fatalf("expected hybrid search results with 林雅婷, got %d cards", len(hybridRes.SearchResults))
		}

		// 4b: Broad Search Fallback
		broadRes, err := engine.RunSearch(ctx, "demo", "請問有沒有新竹科學園區做 AI Chip 晶片的聯絡人？")
		if err != nil {
			t.Fatalf("RunSearch broad fallback failed: %v", err)
		}
		if len(broadRes.SearchResults) == 0 || !strings.Contains(broadRes.FlexCarouselJSON, "張志豪") {
			t.Fatalf("expected broad search fallback results with 張志豪 (TSMC), got %d cards", len(broadRes.SearchResults))
		}

		// 4c: Personalized Follow-up Email Draft
		emailRes, err := engine.RunEmailDraft(ctx, "demo", "card-seed-1")
		if err != nil {
			t.Fatalf("RunEmailDraft failed: %v", err)
		}
		if !bytes.Contains([]byte(emailRes.DraftEmailText), []byte("2026 Cloud Summit Taipei")) ||
			!bytes.Contains([]byte(emailRes.DraftEmailText), []byte("Google Cloud Taiwan")) {
			t.Fatalf("expected email draft grounded on 2026 Cloud Summit Taipei and Google Cloud Taiwan, got: %s", emailRes.DraftEmailText)
		}
	})
}
