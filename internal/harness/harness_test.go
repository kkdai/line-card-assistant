package harness

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestValidateAllHarnessTrees(t *testing.T) {
	dirs := []struct {
		relPath   string
		minSkills int
	}{
		{relPath: "../../harness/base", minSkills: 0},
		{relPath: "../../harness/evolved/k1_single_step", minSkills: 2},
		{relPath: "../../harness/evolved/k2_parallel", minSkills: 4},
		{relPath: "../../harness/evolved/k2_sequential_memory", minSkills: 4},
	}

	for _, tc := range dirs {
		t.Run(tc.relPath, func(t *testing.T) {
			abs, err := filepath.Abs(tc.relPath)
			if err != nil {
				t.Fatalf("filepath.Abs(%s): %v", tc.relPath, err)
			}
			h, err := LoadFromDir(abs)
			if err != nil {
				t.Fatalf("LoadFromDir(%s): %v", abs, err)
			}
			if err := h.Validate(); err != nil {
				t.Fatalf("Validate(%s) failed: %v", abs, err)
			}
			if h.AlwaysOnBytes() <= 0 || h.AlwaysOnBytes() > AlwaysOnMaxBytes {
				t.Fatalf("AlwaysOnBytes(%s) = %d, want (0, %d]", abs, h.AlwaysOnBytes(), AlwaysOnMaxBytes)
			}
			if len(h.Skills) < tc.minSkills {
				t.Fatalf("len(Skills) = %d, want >= %d", len(h.Skills), tc.minSkills)
			}
		})
	}
}

func TestValidateEnforcesSurfaceCapAndSkillTriggerRules(t *testing.T) {
	// 1. Exceeding 24,000-byte always-on surface cap must fail validation
	oversized := strings.Repeat("A", AlwaysOnMaxBytes+100)
	hCap, err := FromTreeMap("test://oversized", map[string]string{
		"systemprompt.md":                  oversized,
		"guardrails.md":                    "# Guardrails",
		"LongTermMEMORY.md":                "# Memory",
		"tool_descriptions/tool_guidance.md": "# Tools",
	})
	if err != nil {
		t.Fatalf("FromTreeMap oversized: %v", err)
	}
	if err := hCap.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds cap") {
		t.Fatalf("expected surface cap error, got: %v", err)
	}

	// 2. Unreferenced skill in systemprompt.md / guardrails.md must fail validation
	hUnref, err := FromTreeMap("test://unref-skill", map[string]string{
		"systemprompt.md":                  "# Core Prompt without skill trigger",
		"guardrails.md":                    "# Guardrails",
		"LongTermMEMORY.md":                "# Memory",
		"tool_descriptions/tool_guidance.md": "# Tools",
		"skills/orphan_skill/SKILL.md":     "---\nname: orphan_skill\ndescription: Orphan skill\n---\nBody",
	})
	if err != nil {
		t.Fatalf("FromTreeMap unref: %v", err)
	}
	if err := hUnref.Validate(); err == nil || !strings.Contains(err.Error(), "never referenced") {
		t.Fatalf("expected unreferenced skill error, got: %v", err)
	}
}

func TestComposeNodePromptAndSkillMatching(t *testing.T) {
	h, err := LoadFromDir("../../harness/evolved/k2_sequential_memory")
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	prompt, skills := h.ComposeNodePrompt("VisionOCRNode", "請辨識這張名片圖片", "ingest:jp_sony_sato")
	if len(skills) == 0 || skills[0] != "jp_card_ocr" {
		t.Fatalf("expected jp_card_ocr skill for VisionOCRNode, got %v", skills)
	}
	if !strings.Contains(prompt, "[Active Skill - jp_card_ocr]") {
		t.Fatalf("expected prompt to contain [Active Skill - jp_card_ocr]")
	}
	if !strings.Contains(prompt, "[Current Node: VisionOCRNode]") {
		t.Fatalf("expected prompt to contain [Current Node: VisionOCRNode]")
	}
}

func TestFormatSpanTrajectory(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test-tracer")

	ctx := context.Background()
	_, span1 := tracer.Start(ctx, "workflow.node.RouterNode")
	span1.SetAttributes(
		attribute.String("adk.workflow.node", "RouterNode"),
		attribute.String("adk.workflow.route", "ingest"),
	)
	span1.End()

	_, span2 := tracer.Start(ctx, "workflow.node.VisionOCRNode")
	span2.SetAttributes(
		attribute.String("adk.workflow.node", "VisionOCRNode"),
		attribute.String("adk.card.name", "佐藤 健一 (Kenichi Sato)"),
		attribute.StringSlice("adk.harness.skills", []string{"jp_card_ocr"}),
	)
	span2.SetStatus(codes.Error, "simulated ocr warning")
	span2.End()

	traj := FormatSpanTrajectory("challenge_jp_bilingual_ingest", "ingest:jp_sony_sato", exporter.GetSpans(), "final preview json")
	for _, want := range []string{
		"# ADK Go Graph Workflow Execution Trajectory — Task `challenge_jp_bilingual_ingest`",
		"[ACTION #1] workflow.node.RouterNode",
		`[OBSERVATION #1] node=RouterNode route="ingest"`,
		"[ACTION #2] workflow.node.VisionOCRNode (active_skills=[jp_card_ocr])",
		`[ERROR #2] node=VisionOCRNode status="simulated ocr warning"`,
	} {
		if !strings.Contains(traj, want) {
			t.Errorf("trajectory missing %q\nfull trajectory:\n%s", want, traj)
		}
	}
}
