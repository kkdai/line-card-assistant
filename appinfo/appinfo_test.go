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

package appinfo

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"log"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	remoteagent "google.golang.org/adk/v2/agent/remoteagent/v2"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/agent/workflowagents/loopagent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/agent/workflowagents/sequentialagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/workflow"
)

// fakeModel stands in for a real model so no test needs credentials. No test
// runs an invocation; they only inspect structure.
type fakeModel struct{}

func (fakeModel) Name() string { return "fake-model" }

func (fakeModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {}
}

type echoArgs struct {
	Text string `json:"text" jsonschema:"the text to echo"`
}

type echoResult struct {
	Text string `json:"text"`
}

func echo(_ agent.Context, a echoArgs) (echoResult, error) {
	return echoResult(a), nil
}

func makeFunctionalTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, err := functiontool.New(functiontool.Config{Name: name, Description: "description of " + name}, echo)
	if err != nil {
		t.Fatalf("functiontool.New(%q): %v", name, err)
	}
	return tl
}

func makeLLMAgent(t *testing.T, name string, tools ...tool.Tool) agent.Agent {
	t.Helper()
	return makeLLMAgentFromCfg(t, llmagent.Config{
		Name:        name,
		Description: "description of " + name,
		Instruction: "instruction of " + name,
		Tools:       tools,
	})
}

func makeLLMAgentFromCfg(t *testing.T, cfg llmagent.Config) agent.Agent {
	t.Helper()
	if cfg.Model == nil {
		cfg.Model = fakeModel{}
	}
	a, err := llmagent.New(cfg)
	if err != nil {
		t.Fatalf("llmagent.New(%q): %v", cfg.Name, err)
	}
	return a
}

// makeRemoteA2AAgent builds a remote A2A agent. The card is never fetched: no test runs
// an invocation, they only inspect structure.
func makeRemoteA2AAgent(t *testing.T, name string) agent.Agent {
	t.Helper()
	a, err := remoteagent.NewA2A(remoteagent.A2AConfig{
		Name:        name,
		Description: "description of " + name,
		AgentCardProvider: remoteagent.NewAgentCardProvider(
			"http://127.0.0.1:1/.well-known/agent-card.json"),
	})
	if err != nil {
		t.Fatalf("remoteagent.NewA2A(%q): %v", name, err)
	}
	return a
}

func makeSequentialAgent(t *testing.T, name string, subs ...agent.Agent) agent.Agent {
	t.Helper()
	a, err := sequentialagent.New(sequentialagent.Config{
		AgentConfig: agent.Config{Name: name, Description: "description of " + name, SubAgents: subs},
	})
	if err != nil {
		t.Fatalf("sequentialagent.New(%q): %v", name, err)
	}
	return a
}

func makeAgentNode(t *testing.T, a agent.Agent) *workflow.AgentNode {
	t.Helper()
	n, err := workflow.NewAgentNode(a, workflow.NodeConfig{})
	if err != nil {
		t.Fatalf("workflow.NewAgentNode(%q): %v", a.Name(), err)
	}
	return n
}

func makeWorkflowAgent(t *testing.T, cfg workflowagent.Config) agent.Agent {
	t.Helper()
	cfg.Description = "description of " + cfg.Name
	a, err := workflowagent.New(cfg)
	if err != nil {
		t.Fatalf("workflowagent.New(%q): %v", cfg.Name, err)
	}
	return a
}

var ignorePropertyOrder = cmpopts.IgnoreFields(jsonschema.Schema{}, "PropertyOrder")

// wantEchoTool is the declaration BuildAppInfo reports for a tool built by
// makeFunctionalTool.
func wantEchoTool(name string) *genai.Tool {
	return &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{
		Name:        name,
		Description: "description of " + name,
		ParametersJsonSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"text": {Type: "string", Description: "the text to echo"},
			},
			Required:             []string{"text"},
			AdditionalProperties: falseSchema(),
		},
		ResponseJsonSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"text": {Type: "string"},
			},
			Required:             []string{"text"},
			AdditionalProperties: falseSchema(),
		},
	}}}
}

// wantAgentTool is the declaration for an agent wrapped by agenttool.New.
func wantAgentTool(name string) *genai.Tool {
	return &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{
		Name:        name,
		Description: "description of " + name,
		Parameters: &genai.Schema{
			Type:       genai.TypeObject,
			Properties: map[string]*genai.Schema{"request": {Type: genai.TypeString}},
			Required:   []string{"request"},
		},
	}}}
}

// falseSchema is what "additionalProperties": false unmarshals to.
func falseSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Not: &jsonschema.Schema{}}
}

func TestBuildAppInfoReportsTheRootAgentName(t *testing.T) {
	root := makeLLMAgent(t, "the_root")

	info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
	if err != nil {
		t.Fatalf("BuildAppInfo: %v", err)
	}
	if info.RootAgentName != "the_root" {
		t.Errorf("RootAgentName = %q, want %q", info.RootAgentName, "the_root")
	}
}

func TestBuildAppInfoReportsLlmAgentsBelowComposites(t *testing.T) {
	t.Run("composite_between_llm_agents", func(t *testing.T) {
		// A non-LlmAgent between two LlmAgents.
		reporter := makeLLMAgent(t, "reporter", makeFunctionalTool(t, "get_weather"))
		pipeline := makeSequentialAgent(t, "pipeline", reporter)
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "demo",
			Description: "description of demo",
			Instruction: "instruction of demo",
			Tools:       []tool.Tool{makeFunctionalTool(t, "get_current_time")},
			SubAgents:   []agent.Agent{pipeline},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"demo": {
				Name:        "demo",
				Description: "description of demo",
				Instruction: "instruction of demo",
				Tools:       []*genai.Tool{wantEchoTool("get_current_time")},
				SubAgents:   []string{},
			},
			"reporter": {
				Name:        "reporter",
				Description: "description of reporter",
				Instruction: "instruction of reporter",
				Tools:       []*genai.Tool{wantEchoTool("get_weather")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("composite_root", func(t *testing.T) {
		// A non-LlmAgent root.
		assistant := makeLLMAgent(t, "assistant", makeFunctionalTool(t, "get_weather"))
		root := makeWorkflowAgent(t, workflowagent.Config{
			Name:      "demo",
			Edges:     workflow.Chain(workflow.Start, makeAgentNode(t, assistant)),
			SubAgents: []agent.Agent{assistant},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"assistant": {
				Name:        "assistant",
				Description: "description of assistant",
				Instruction: "instruction of assistant",
				Tools:       []*genai.Tool{wantEchoTool("get_weather")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("deeply_nested_composites", func(t *testing.T) {
		// Several composite layers with no LlmAgent between them.
		leaf := makeLLMAgent(t, "leaf", makeFunctionalTool(t, "leaf_tool"))
		lp, err := loopagent.New(loopagent.Config{
			AgentConfig:   agent.Config{Name: "inner_loop", SubAgents: []agent.Agent{leaf}},
			MaxIterations: 2,
		})
		if err != nil {
			t.Fatalf("loopagent.New: %v", err)
		}
		par, err := parallelagent.New(parallelagent.Config{
			AgentConfig: agent.Config{Name: "inner_parallel", SubAgents: []agent.Agent{lp}},
		})
		if err != nil {
			t.Fatalf("parallelagent.New: %v", err)
		}
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "root",
			Instruction: "instruction of root",
			SubAgents:   []agent.Agent{makeSequentialAgent(t, "outer_pipeline", par)},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"leaf": {
				Name:        "leaf",
				Description: "description of leaf",
				Instruction: "instruction of leaf",
				Tools:       []*genai.Tool{wantEchoTool("leaf_tool")},
				SubAgents:   []string{},
			},
			"root": {
				Name:        "root",
				Description: "",
				Instruction: "instruction of root",
				Tools:       []*genai.Tool{},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("sequential_agent_root", func(t *testing.T) {
		// A SequentialAgent root.
		root := makeSequentialAgent(t, "pipeline", makeLLMAgent(t, "first"), makeLLMAgent(t, "second"))
		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"first": {
				Name:        "first",
				Description: "description of first",
				Instruction: "instruction of first",
				Tools:       []*genai.Tool{},
				SubAgents:   []string{},
			},
			"second": {
				Name:        "second",
				Description: "description of second",
				Instruction: "instruction of second",
				Tools:       []*genai.Tool{},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestBuildAppInfoFollowsWorkflowEdges covers agents a workflowagent holds only
// as graph nodes.
func TestBuildAppInfoFollowsWorkflowEdges(t *testing.T) {
	t.Run("node_agents_absent_from_subagents", func(t *testing.T) {
		// Node agents the workflowagent does not report as sub-agents.
		drafter := makeLLMAgent(t, "drafter", makeFunctionalTool(t, "draft"))
		reviewer := makeLLMAgent(t, "reviewer", makeFunctionalTool(t, "review"))
		root := makeWorkflowAgent(t, workflowagent.Config{
			Name:  "writing_flow",
			Edges: workflow.Chain(workflow.Start, makeAgentNode(t, drafter), makeAgentNode(t, reviewer)),
		})

		if len(root.SubAgents()) != 0 {
			t.Fatalf("fixture is wrong: workflowagent reports %d sub-agent(s)", len(root.SubAgents()))
		}
		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"drafter": {
				Name:        "drafter",
				Description: "description of drafter",
				Instruction: "instruction of drafter",
				Tools:       []*genai.Tool{wantEchoTool("draft")},
				SubAgents:   []string{},
			},
			"reviewer": {
				Name:        "reviewer",
				Description: "description of reviewer",
				Instruction: "instruction of reviewer",
				Tools:       []*genai.Tool{wantEchoTool("review")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("nested_sub_workflow", func(t *testing.T) {
		// An agent inside a nested sub-workflow.
		deep := makeLLMAgent(t, "deep", makeFunctionalTool(t, "deep_tool"))
		sub, err := workflow.NewWorkflowNode("subflow", workflow.Chain(workflow.Start, makeAgentNode(t, deep)))
		if err != nil {
			t.Fatalf("workflow.NewWorkflowNode: %v", err)
		}
		outer := makeLLMAgent(t, "outer", makeFunctionalTool(t, "outer_tool"))
		root := makeWorkflowAgent(t, workflowagent.Config{
			Name:  "nesting_flow",
			Edges: workflow.Chain(workflow.Start, makeAgentNode(t, outer), sub),
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"deep": {
				Name:        "deep",
				Description: "description of deep",
				Instruction: "instruction of deep",
				Tools:       []*genai.Tool{wantEchoTool("deep_tool")},
				SubAgents:   []string{},
			},
			"outer": {
				Name:        "outer",
				Description: "description of outer",
				Instruction: "instruction of outer",
				Tools:       []*genai.Tool{wantEchoTool("outer_tool")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("fan_out_join_tool_and_function_nodes", func(t *testing.T) {
		// Fan-out, join, tool and function nodes.
		alpha := makeAgentNode(t, makeLLMAgent(t, "alpha", makeFunctionalTool(t, "alpha_tool")))
		beta := makeAgentNode(t, makeLLMAgent(t, "beta", makeFunctionalTool(t, "beta_tool")))
		gather := workflow.NewJoinNode("gather")
		lookup, err := workflow.NewToolNode(makeFunctionalTool(t, "lookup_tool"), workflow.NodeConfig{})
		if err != nil {
			t.Fatalf("workflow.NewToolNode: %v", err)
		}
		format := workflow.NewFunctionNode("format",
			func(_ agent.Context, in map[string]any) (string, error) { return "", nil },
			workflow.NodeConfig{})

		eb := workflow.NewEdgeBuilder()
		eb.AddFanOut(workflow.Start, alpha, beta)
		eb.AddFanIn(gather, alpha, beta)
		eb.Add(gather, format)
		eb.Add(format, lookup)
		root := makeWorkflowAgent(t, workflowagent.Config{Name: "fanout_flow", Edges: eb.Build()})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"alpha": {
				Name:        "alpha",
				Description: "description of alpha",
				Instruction: "instruction of alpha",
				Tools:       []*genai.Tool{wantEchoTool("alpha_tool")},
				SubAgents:   []string{},
			},
			"beta": {
				Name:        "beta",
				Description: "description of beta",
				Instruction: "instruction of beta",
				Tools:       []*genai.Tool{wantEchoTool("beta_tool")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("agent_reached_by_two_edges", func(t *testing.T) {
		// An agent reached by two edges is visited once.
		assistant := makeLLMAgent(t, "assistant", makeFunctionalTool(t, "get_weather"))
		root := makeWorkflowAgent(t, workflowagent.Config{
			Name:      "flow",
			Edges:     workflow.Chain(workflow.Start, makeAgentNode(t, assistant)),
			SubAgents: []agent.Agent{assistant},
		})

		var agents map[string]AgentInfo
		logged := captureLog(t, func() {
			info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
			if err != nil {
				t.Fatalf("BuildAppInfo: %v", err)
			}
			agents = info.Agents
		})

		want := map[string]AgentInfo{
			"assistant": {
				Name:        "assistant",
				Description: "description of assistant",
				Instruction: "instruction of assistant",
				Tools:       []*genai.Tool{wantEchoTool("get_weather")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
		if strings.Contains(logged, "two different agents are both named") {
			t.Errorf("revisiting one agent was reported as a name clash; log was %q", logged)
		}
	})
}

func TestBuildAppInfoFollowsAgentTools(t *testing.T) {
	t.Run("agent_as_a_tool", func(t *testing.T) {
		// An agent wrapped by agenttool.New.
		specialist := makeLLMAgent(t, "specialist", makeFunctionalTool(t, "specialist_tool"))
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "coordinator",
			Instruction: "instruction of coordinator",
			Tools:       []tool.Tool{makeFunctionalTool(t, "coordinator_tool"), agenttool.New(specialist, nil)},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		// An agent-tool declares itself under the wrapped agent's name.
		want := map[string]AgentInfo{
			"coordinator": {
				Name:        "coordinator",
				Description: "",
				Instruction: "instruction of coordinator",
				Tools:       []*genai.Tool{wantEchoTool("coordinator_tool"), wantAgentTool("specialist")},
				SubAgents:   []string{},
			},
			"specialist": {
				Name:        "specialist",
				Description: "description of specialist",
				Instruction: "instruction of specialist",
				Tools:       []*genai.Tool{wantEchoTool("specialist_tool")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("agent_tool_wrapping_a_pipeline", func(t *testing.T) {
		// An agent tool wrapping a pipeline.
		pipeline := makeSequentialAgent(t, "pipeline",
			makeLLMAgent(t, "stage_a", makeFunctionalTool(t, "a_tool")),
			makeLLMAgent(t, "stage_b", makeFunctionalTool(t, "b_tool")))
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "root",
			Instruction: "instruction of root",
			Tools:       []tool.Tool{agenttool.New(pipeline, nil)},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"root": {
				Name:        "root",
				Description: "",
				Instruction: "instruction of root",
				Tools:       []*genai.Tool{wantAgentTool("pipeline")},
				SubAgents:   []string{},
			},
			"stage_a": {
				Name:        "stage_a",
				Description: "description of stage_a",
				Instruction: "instruction of stage_a",
				Tools:       []*genai.Tool{wantEchoTool("a_tool")},
				SubAgents:   []string{},
			},
			"stage_b": {
				Name:        "stage_b",
				Description: "description of stage_b",
				Instruction: "instruction of stage_b",
				Tools:       []*genai.Tool{wantEchoTool("b_tool")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("agent_as_both_sub_agent_and_tool", func(t *testing.T) {
		// The same agent as both a sub-agent and a tool is reported once.
		shared := makeLLMAgent(t, "shared", makeFunctionalTool(t, "shared_tool"))
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "root",
			Instruction: "instruction of root",
			SubAgents:   []agent.Agent{shared},
			Tools:       []tool.Tool{agenttool.New(shared, nil)},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"root": {
				Name:        "root",
				Description: "",
				Instruction: "instruction of root",
				Tools:       []*genai.Tool{wantAgentTool("shared")},
				SubAgents:   []string{"shared"},
			},
			"shared": {
				Name:        "shared",
				Description: "description of shared",
				Instruction: "instruction of shared",
				Tools:       []*genai.Tool{wantEchoTool("shared_tool")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("duplicate_agent_name", func(t *testing.T) {
		// Two agents sharing a name: one is kept and the clash is reported.
		subHelper := makeLLMAgent(t, "helper", makeFunctionalTool(t, "sub_helper_tool"))
		toolHelper := makeLLMAgent(t, "helper", makeFunctionalTool(t, "tool_helper_tool"))
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "root",
			Instruction: "instruction of root",
			SubAgents:   []agent.Agent{subHelper},
			Tools:       []tool.Tool{agenttool.New(toolHelper, nil)},
		})

		var agents map[string]AgentInfo
		logged := captureLog(t, func() {
			info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
			if err != nil {
				t.Fatalf("BuildAppInfo: %v", err)
			}
			agents = info.Agents
		})

		want := map[string]AgentInfo{
			"helper": {
				Name:        "helper",
				Description: "description of helper",
				Instruction: "instruction of helper",
				Tools:       []*genai.Tool{wantEchoTool("sub_helper_tool")},
				SubAgents:   []string{},
			},
			"root": {
				Name:        "root",
				Description: "",
				Instruction: "instruction of root",
				Tools:       []*genai.Tool{wantAgentTool("helper")},
				SubAgents:   []string{"helper"},
			},
		}
		if diff := cmp.Diff(want, agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
		if !strings.Contains(logged, "two different agents are both named") {
			t.Errorf("name clash was not reported; log was %q", logged)
		}
	})
}

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	fn()
	return buf.String()
}

func TestBuildAppInfoAgentFields(t *testing.T) {
	t.Run("instruction_provider", func(t *testing.T) {
		// An InstructionProvider is named, not reported empty.
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:                "dynamic",
			Description:         "description of dynamic",
			InstructionProvider: computeInstruction,
			Tools:               []tool.Tool{makeFunctionalTool(t, "dyn_tool")},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		// The provider cannot be evaluated without a session, so app-info
		// reports its Go function name in place of the instruction.
		want := map[string]AgentInfo{
			"dynamic": {
				Name:        "dynamic",
				Description: "description of dynamic",
				Instruction: "<InstructionProvider: appinfo.computeInstruction>",
				Tools:       []*genai.Tool{wantEchoTool("dyn_tool")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("toolsets_expanded", func(t *testing.T) {
		// Toolsets are expanded and a failing one is skipped.
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "toolset_agent",
			Instruction: "instruction of toolset_agent",
			Tools:       []tool.Tool{makeFunctionalTool(t, "static_tool")},
			Toolsets: []tool.Toolset{
				staticToolset{name: "mcp_like", tools: []tool.Tool{makeFunctionalTool(t, "ts_one"), makeFunctionalTool(t, "ts_two")}},
				failingToolset{name: "unreachable"},
			},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"toolset_agent": {
				Name:        "toolset_agent",
				Description: "",
				Instruction: "instruction of toolset_agent",
				Tools:       []*genai.Tool{wantEchoTool("static_tool"), wantEchoTool("ts_one"), wantEchoTool("ts_two")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("tool_without_declaration", func(t *testing.T) {
		// A tool without a declaration is omitted, the agent survives.
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "mixed_tools",
			Instruction: "instruction of mixed_tools",
			Tools:       []tool.Tool{makeFunctionalTool(t, "declared"), opaqueTool{name: "opaque"}},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"mixed_tools": {
				Name:        "mixed_tools",
				Description: "",
				Instruction: "instruction of mixed_tools",
				Tools:       []*genai.Tool{wantEchoTool("declared")},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("bare_llm_agent", func(t *testing.T) {
		// An LlmAgent with nothing set is still reported.
		root := makeLLMAgentFromCfg(t, llmagent.Config{Name: "bare"})
		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"bare": {
				Name:        "bare",
				Description: "",
				Instruction: "",
				Tools:       []*genai.Tool{},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("adk_injected_tools", func(t *testing.T) {
		// ADK-injected tools are reported.
		//
		// A task-mode sub-agent gets finish_task, and its parent gets a
		// delegation tool named after it. Both are in the list the request
		// processor shows the model.
		taskSub := makeLLMAgentFromCfg(t, llmagent.Config{
			Name: "task_sub", Instruction: "instruction of task_sub", Mode: llmagent.ModeTask,
		})
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "coordinator",
			Instruction: "instruction of coordinator",
			Tools:       []tool.Tool{makeFunctionalTool(t, "real_tool")},
			SubAgents:   []agent.Agent{taskSub},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"coordinator": {
				Name:        "coordinator",
				Instruction: "instruction of coordinator",
				Tools: []*genai.Tool{
					wantEchoTool("real_tool"),
					{FunctionDeclarations: []*genai.FunctionDeclaration{{
						Name: "task_sub",
						Description: "IMPORTANT: This tool delegates execution to a specialized " +
							"agent. Do NOT call this tool in parallel with any other tools.",
						Parameters: &genai.Schema{
							Type: genai.TypeObject,
							Properties: map[string]*genai.Schema{
								"request": {
									Type:        genai.TypeString,
									Description: "Detailed instructions or context for the task sub-agent.",
								},
							},
							Required: []string{"request"},
						},
						ResponseJsonSchema: &genai.Schema{Type: genai.TypeString},
					}}},
				},
				SubAgents: []string{"task_sub"},
			},
			"task_sub": {
				Name:        "task_sub",
				Instruction: "instruction of task_sub",
				Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name: "finish_task",
					Description: "Signal that this agent has completed its delegated task. " +
						"Call this when you have finished your delegated task.",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"result": {
								Type:        genai.TypeString,
								Description: "A brief summary of what the agent accomplished.",
							},
						},
						Required: []string{"result"},
					},
				}}}},
				SubAgents: []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("remote_agent", func(t *testing.T) {
		// A remote agent is not reported: it runs no local model.
		root := makeLLMAgentFromCfg(t, llmagent.Config{
			Name:        "root",
			Instruction: "instruction of root",
			SubAgents:   []agent.Agent{makeRemoteA2AAgent(t, "remote_helper")},
		})

		info, err := BuildAppInfo(agent.NewSingleLoader(root), root.Name())
		if err != nil {
			t.Fatalf("BuildAppInfo: %v", err)
		}
		want := map[string]AgentInfo{
			"root": {
				Name:        "root",
				Description: "",
				Instruction: "instruction of root",
				Tools:       []*genai.Tool{},
				SubAgents:   []string{},
			},
		}
		if diff := cmp.Diff(want, info.Agents, ignorePropertyOrder); diff != "" {
			t.Errorf("BuildAppInfo() agents mismatch (-want +got):\n%s", diff)
		}
	})
}

// staticToolset returns a fixed set of tools, like an MCP toolset would.
type staticToolset struct {
	name  string
	tools []tool.Tool
}

func (s staticToolset) Name() string { return s.name }

func (s staticToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) { return s.tools, nil }

// failingToolset always errors when enumerated, like an MCP toolset whose server
// is unreachable at introspection time.
type failingToolset struct{ name string }

func (f failingToolset) Name() string { return f.name }

func (f failingToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	return nil, fmt.Errorf("toolset %q: connection refused", f.name)
}

func computeInstruction(agent.ReadonlyContext) (string, error) { return "computed", nil }

// opaqueTool does not implement Declaration(), so app-info has nothing to report
// for it.
type opaqueTool struct{ name string }

func (t opaqueTool) Name() string        { return t.name }
func (t opaqueTool) Description() string { return "opaque " + t.name }
func (t opaqueTool) IsLongRunning() bool { return false }

func TestNormalizePathPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"root", "/", ""},                   // root maps to empty
		{"empty", "", ""},                   // empty maps to empty
		{"bare_word", "api", "/api"},        // a bare word gets a leading slash
		{"trailing_slash", "/api/", "/api"}, // a trailing slash is trimmed
		{"nested", "/foo/bar/", "/foo/bar"}, // a nested prefix is preserved
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePathPrefix(tt.in); got != tt.want {
				t.Errorf("normalizePathPrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLauncherRoute(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		// The default and an explicit root both serve at the root; a custom
		// prefix is prepended.
		{"default", nil, appInfoSuffix},
		{"explicit_root", []string{"-path_prefix", "/"}, appInfoSuffix},
		{"custom_prefix", []string{"-path_prefix", "/api"}, "/api" + appInfoSuffix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, ok := NewLauncher().(*appInfoLauncher)
			if !ok {
				t.Fatalf("NewLauncher() is not *appInfoLauncher")
			}
			if _, err := l.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%v): %v", tt.args, err)
			}
			if got := l.route(); got != tt.want {
				t.Errorf("route() = %q, want %q", got, tt.want)
			}
		})
	}
}
