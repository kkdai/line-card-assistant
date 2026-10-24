package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kkdai/line-card-assistant/app"
	"github.com/kkdai/line-card-assistant/internal/harness"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type evalDataset struct {
	EvalCases []evalCase `json:"eval_cases"`
}

type evalCase struct {
	EvalCaseID string `json:"eval_case_id"`
	Domain     string `json:"domain,omitempty"`
	Prompt     *struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"prompt,omitempty"`
	Conversation []struct {
		UserContent struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"user_content"`
	} `json:"conversation,omitempty"`
}

func (c evalCase) promptText() string {
	if c.Prompt != nil {
		var parts []string
		for _, p := range c.Prompt.Parts {
			if strings.TrimSpace(p.Text) != "" {
				parts = append(parts, strings.TrimSpace(p.Text))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	for _, turn := range c.Conversation {
		var parts []string
		for _, p := range turn.UserContent.Parts {
			if strings.TrimSpace(p.Text) != "" {
				parts = append(parts, strings.TrimSpace(p.Text))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

// DeliverableSnapshot captures the final ADK Session.State and reply text for a single task execution.
type DeliverableSnapshot struct {
	TaskID           string         `json:"task_id"`
	UserPrompt       string         `json:"user_prompt"`
	HarnessRoot      string         `json:"harness_root"`
	AlwaysOnBytes    int            `json:"always_on_bytes"`
	FinalText        string         `json:"final_text"`
	SessionState     map[string]any `json:"session_state"`
	ExecutedNodeList []string       `json:"executed_nodes"`
}

func main() {
	datasetPath := flag.String("dataset", "eval/datasets/harness2-challenge-dataset.json", "Path to evaluation dataset JSON")
	taskID := flag.String("task-id", "", "Specific eval_case_id to execute")
	harnessDir := flag.String("harness-dir", "harness/base", "Path to c1..c8 Harness directory")
	runDir := flag.String("run-dir", "", "Output directory for answer.txt, deliverable.json, and trajectory.md")
	validateOnly := flag.Bool("validate-only", false, "Only validate the c1..c8 Harness directory and exit")
	flag.Parse()

	_ = os.Setenv("DRY_RUN", "true")
	absHarness, err := filepath.Abs(*harnessDir)
	if err != nil {
		fatalf("resolve harness-dir: %v", err)
	}
	_ = os.Setenv(harness.EnvHarnessDir, absHarness)

	h, err := harness.LoadFromDir(absHarness)
	if err != nil {
		fatalf("load harness from %s: %v", absHarness, err)
	}
	if err := h.Validate(); err != nil {
		fatalf("validate harness: %v", err)
	}
	if *validateOnly {
		fmt.Printf("OK (%d always-on bytes)\n", h.AlwaysOnBytes())
		return
	}

	rawDS, err := os.ReadFile(*datasetPath)
	if err != nil {
		fatalf("read dataset %s: %v", *datasetPath, err)
	}
	var ds evalDataset
	if err := json.Unmarshal(rawDS, &ds); err != nil {
		fatalf("parse dataset %s: %v", *datasetPath, err)
	}

	var target *evalCase
	for i := range ds.EvalCases {
		if ds.EvalCases[i].EvalCaseID == *taskID {
			target = &ds.EvalCases[i]
			break
		}
	}
	if target == nil {
		fatalf("task-id %q not found in %s", *taskID, *datasetPath)
	}

	if *runDir == "" {
		fatalf("--run-dir is required")
	}
	if err := os.MkdirAll(*runDir, 0o755); err != nil {
		fatalf("mkdir run-dir %s: %v", *runDir, err)
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
	}()

	ctx := context.Background()
	rootAgent, err := app.NewRootAgent(ctx)
	if err != nil {
		fatalf("NewRootAgent: %v", err)
	}

	sessionSvc := session.InMemoryService()
	artifactSvc := artifact.InMemoryService()
	adkRunner, err := runner.New(runner.Config{
		AppName:           "line-card-assistant",
		Agent:             rootAgent,
		SessionService:    sessionSvc,
		ArtifactService:   artifactSvc,
		MemoryService:     memory.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		fatalf("runner.New: %v", err)
	}

	promptText := target.promptText()
	userID := "demo"
	sessionID := "harness2-" + target.EvalCaseID
	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: promptText},
		},
	}

	var finalTexts []string
	mergedState := make(map[string]any)
	for ev, runErr := range adkRunner.Run(ctx, userID, sessionID, msg, agent.RunConfig{}) {
		if runErr != nil {
			fatalf("runner.Run(%s): %v", target.EvalCaseID, runErr)
		}
		if ev == nil {
			continue
		}
		for k, v := range ev.Actions.StateDelta {
			mergedState[k] = v
		}
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p != nil && strings.TrimSpace(p.Text) != "" && !strings.HasPrefix(strings.TrimSpace(p.Text), "[") {
					finalTexts = append(finalTexts, strings.TrimSpace(p.Text))
				}
			}
		}
	}

	finalDeliverable := ""
	if len(finalTexts) > 0 {
		finalDeliverable = finalTexts[len(finalTexts)-1]
	}

	spans := exporter.GetSpans()
	var executedNodes []string
	for _, s := range spans {
		if strings.HasPrefix(s.Name, "workflow.node.") {
			executedNodes = append(executedNodes, strings.TrimPrefix(s.Name, "workflow.node."))
		}
	}

	snapshot := DeliverableSnapshot{
		TaskID:           target.EvalCaseID,
		UserPrompt:       promptText,
		HarnessRoot:      absHarness,
		AlwaysOnBytes:    h.AlwaysOnBytes(),
		FinalText:        finalDeliverable,
		SessionState:     mergedState,
		ExecutedNodeList: executedNodes,
	}

	deliverableBytes, _ := json.MarshalIndent(snapshot, "", "  ")
	answerContent := fmt.Sprintf("=== FINAL DELIVERABLE (%s) ===\n%s\n\n=== SESSION STATE SNAPSHOT ===\n%s\n",
		target.EvalCaseID, finalDeliverable, string(deliverableBytes))

	traj := harness.FormatSpanTrajectory(target.EvalCaseID, promptText, spans, finalDeliverable)

	if err := os.WriteFile(filepath.Join(*runDir, "answer.txt"), []byte(answerContent), 0o644); err != nil {
		fatalf("write answer.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*runDir, "deliverable.json"), deliverableBytes, 0o644); err != nil {
		fatalf("write deliverable.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*runDir, "trajectory.md"), []byte(traj), 0o644); err != nil {
		fatalf("write trajectory.md: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "harness-runner error: "+format+"\n", args...)
	os.Exit(1)
}
