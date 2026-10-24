package harness

import (
	"fmt"
	"sort"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// FormatSpanTrajectory converts recorded OpenTelemetry workflow node spans (from Phase 5 telemetry.go)
// into the [ACTION] / [OBSERVATION] / [ERROR] trajectory format consumed by Google Research Harness²
// (see harness2/core/recursion.py ProposePrompt & ComposePrompt).
//
// Crucially, this formatter preserves the zero-leakage barrier required by Harness²:
// it emits only behavioral traces (node invocations, matched c6 skills, state transitions, routes,
// and error codes) and never includes evaluation rubrics, ground truth, or test scores.
func FormatSpanTrajectory(taskID string, userPrompt string, spans tracetest.SpanStubs, deliverableSummary string) string {
	sorted := make(tracetest.SpanStubs, len(spans))
	copy(sorted, spans)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].StartTime.Before(sorted[j].StartTime)
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# ADK Go Graph Workflow Execution Trajectory — Task `%s`\n", taskID))
	sb.WriteString(fmt.Sprintf("[USER_PROMPT] %s\n\n", strings.TrimSpace(userPrompt)))

	step := 0
	for _, s := range sorted {
		if !strings.HasPrefix(s.Name, "workflow.node.") {
			continue
		}
		step++
		attrs := attrMap(s.Attributes)
		nodeName := attrs["adk.workflow.node"]
		if nodeName == "" {
			nodeName = strings.TrimPrefix(s.Name, "workflow.node.")
		}
		skills := attrs["adk.harness.skills"]
		alwaysOnBytes := attrs["adk.harness.always_on_bytes"]

		actionHeader := fmt.Sprintf("[ACTION #%d] %s", step, s.Name)
		if skills != "" && skills != "[]" {
			actionHeader += fmt.Sprintf(" (active_skills=%s", skills)
			if alwaysOnBytes != "" {
				actionHeader += fmt.Sprintf(", always_on_bytes=%s", alwaysOnBytes)
			}
			actionHeader += ")"
		} else if alwaysOnBytes != "" {
			actionHeader += fmt.Sprintf(" (active_skills=none, always_on_bytes=%s)", alwaysOnBytes)
		}
		sb.WriteString(actionHeader + "\n")

		if s.Status.Code == codes.Error {
			sb.WriteString(fmt.Sprintf("[ERROR #%d] node=%s status=%q\n", step, nodeName, s.Status.Description))
			continue
		}

		obsParts := buildNodeObservationParts(nodeName, attrs)
		sb.WriteString(fmt.Sprintf("[OBSERVATION #%d] node=%s %s\n", step, nodeName, strings.Join(obsParts, " ")))
	}

	if strings.TrimSpace(deliverableSummary) != "" {
		sb.WriteString("\n[DELIVERABLE_SUMMARY]\n")
		sb.WriteString(strings.TrimSpace(deliverableSummary))
		sb.WriteString("\n")
	}
	return sb.String()
}

func attrMap(kvs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		key := string(kv.Key)
		switch kv.Value.Type() {
		case attribute.STRINGSLICE:
			slice := kv.Value.AsStringSlice()
			if len(slice) > 0 {
				out[key] = "[" + strings.Join(slice, ",") + "]"
			} else {
				out[key] = "[]"
			}
		default:
			out[key] = kv.Value.Emit()
		}
	}
	return out
}

func buildNodeObservationParts(nodeName string, attrs map[string]string) []string {
	var parts []string
	if route := attrs["adk.workflow.route"]; route != "" {
		parts = append(parts, fmt.Sprintf("route=%q", route))
	}
	if hitlAction := attrs["adk.hitl.action"]; hitlAction != "" {
		parts = append(parts, fmt.Sprintf("hitl_action=%q", hitlAction))
	}
	if name := attrs["adk.card.name"]; name != "" {
		parts = append(parts, fmt.Sprintf("card_name=%q", name))
	}
	if title := attrs["adk.card.title"]; title != "" {
		parts = append(parts, fmt.Sprintf("card_title=%q", title))
	}
	if company := attrs["adk.card.company"]; company != "" {
		parts = append(parts, fmt.Sprintf("card_company=%q", company))
	}
	if phone := attrs["adk.card.phone"]; phone != "" {
		parts = append(parts, fmt.Sprintf("card_phone=%q", phone))
	}
	if dup := attrs["adk.card.is_duplicate"]; dup != "" {
		parts = append(parts, fmt.Sprintf("is_duplicate=%s", dup))
	}
	if kw := attrs["adk.search.keywords"]; kw != "" {
		parts = append(parts, fmt.Sprintf("search_keywords=%s", kw))
	}
	if matches := attrs["adk.search.matches"]; matches != "" {
		parts = append(parts, fmt.Sprintf("search_matches=%s", matches))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("node=%s status=ok", nodeName))
	}
	return parts
}
