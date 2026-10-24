package workflow

import (
	"context"

	"github.com/kkdai/line-card-assistant/internal/harness"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const workflowTracerName = "github.com/kkdai/line-card-assistant/internal/workflow"

// OpenTelemetry attribute keys emitted on ADK 2.0 Graph Workflow node spans.
// These attributes are exported via observability.go to Google Cloud Trace
// and sunk into BigQuery via deployment/terraform/shared/telemetry.tf.
const (
	AttrWorkflowNode         = attribute.Key("adk.workflow.node")
	AttrWorkflowRoute        = attribute.Key("adk.workflow.route")
	AttrInvocationID         = attribute.Key("adk.invocation.id")
	AttrSessionID            = attribute.Key("adk.session.id")
	AttrUserID               = attribute.Key("adk.user.id")
	AttrCardID               = attribute.Key("adk.card.id")
	AttrCardName             = attribute.Key("adk.card.name")
	AttrCardTitle            = attribute.Key("adk.card.title")
	AttrCardCompany          = attribute.Key("adk.card.company")
	AttrCardPhone            = attribute.Key("adk.card.phone")
	AttrCardDuplicate        = attribute.Key("adk.card.is_duplicate")
	AttrHITLInterrupt        = attribute.Key("adk.hitl.interrupt_id")
	AttrHITLAction           = attribute.Key("adk.hitl.action")
	AttrSearchMatches        = attribute.Key("adk.search.matches")
	AttrSearchKeywords       = attribute.Key("adk.search.keywords")
	AttrHarnessRoot          = attribute.Key("adk.harness.root")
	AttrHarnessSkills        = attribute.Key("adk.harness.skills")
	AttrHarnessAlwaysOnBytes = attribute.Key("adk.harness.always_on_bytes")
)

// composeHarnessPrompt loads the active Harness² tree, composes the node prompt with c1..c8 surface rules and matched skills,
// and attaches OpenTelemetry attributes for Harness² trajectory inspection.
func composeHarnessPrompt(span trace.Span, nodeName, basePrompt, taskText string) (string, *harness.Harness) {
	h := harness.LoadActive()
	composed, skills := h.ComposeNodePrompt(nodeName, basePrompt, taskText)
	if span != nil && h != nil {
		span.SetAttributes(
			AttrHarnessRoot.String(h.RootDir),
			AttrHarnessSkills.StringSlice(skills),
			AttrHarnessAlwaysOnBytes.Int(h.AlwaysOnBytes()),
		)
	}
	return composed, h
}

// startNodeSpan starts an OpenTelemetry span for a single ADK 2.0 Graph Workflow node.
// When Cloud Trace is enabled via SetupObservability (OTLP HTTP to telemetry.googleapis.com),
// each node span appears as a child span under the root workflow invocation trace.
func startNodeSpan(ctx context.Context, nodeName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	baseAttrs := make([]attribute.KeyValue, 0, len(attrs)+1)
	baseAttrs = append(baseAttrs, AttrWorkflowNode.String(nodeName))
	baseAttrs = append(baseAttrs, attrs...)
	return otel.Tracer(workflowTracerName).Start(
		ctx,
		"workflow.node."+nodeName,
		trace.WithAttributes(baseAttrs...),
	)
}

// endNodeSpan records an error status (if err != nil) and ends the span.
func endNodeSpan(span trace.Span, err error, attrs ...attribute.KeyValue) {
	if span == nil {
		return
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}
