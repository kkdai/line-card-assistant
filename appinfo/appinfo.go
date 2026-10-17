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

// Package appinfo adds a GET {path_prefix}/apps/{app_name}/app-info endpoint to
// the ADK Go web server, mirroring ADK Python's experimental AppInfo endpoint.
// ADK Go does not expose this route yet, so it is served here from template code
// via a custom web.Sublauncher. It lets `agents-cli eval` introspect a running
// agent (name, description, per-agent instruction and tool declarations) over
// HTTP, the same way it can for Python agents.
package appinfo

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"unsafe"

	"github.com/gorilla/mux"
	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/cmd/launcher"
	weblauncher "google.golang.org/adk/v2/cmd/launcher/web"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	adkworkflow "google.golang.org/adk/v2/workflow"
)

// AppInfo mirrors adk-python's AppInfo
// https://github.com/google/adk-python/blob/main/src/google/adk/cli/api_server.py
type AppInfo struct {
	Name          string               `json:"name"`
	RootAgentName string               `json:"rootAgentName"`
	Description   string               `json:"description"`
	Language      string               `json:"language"`
	IsComputerUse bool                 `json:"isComputerUse"`
	Agents        map[string]AgentInfo `json:"agents"`
}

// AgentInfo mirrors adk-python's AgentInfo
// https://github.com/google/adk-python/blob/main/src/google/adk/utils/agent_info.py
type AgentInfo struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Instruction string        `json:"instruction"`
	Tools       []*genai.Tool `json:"tools"`
	SubAgents   []string      `json:"subAgents"`
}

// BuildAppInfo walks the agent graph rooted at the app loaded by name and
// assembles the AppInfo payload.
func BuildAppInfo(loader adkagent.Loader, appName string) (*AppInfo, error) {
	root, err := loader.LoadAgent(appName)
	if err != nil {
		return nil, err
	}

	w := &walker{agents: map[string]AgentInfo{}, visited: map[any]bool{}}
	w.walkAll(root)

	return &AppInfo{
		Name:          appName,
		RootAgentName: root.Name(),
		Description:   root.Description(),
		Language:      "go",
		Agents:        w.agents,
	}, nil
}

type walker struct {
	agents  map[string]AgentInfo
	visited map[any]bool
}

// walkAll runs the walk and contains any panic inside it.
func (w *walker) walkAll(root adkagent.Agent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("appinfo: agent walk aborted after %d agent(s): %v", len(w.agents), r)
		}
	}()
	w.walk(root)
}

// walk visits an agent and every agent reachable from it.
// LLM agents are reported.
func (w *walker) walk(a adkagent.Agent) {
	if a == nil {
		return
	}
	// Keyed on identity, since agent can have duplicate names.
	id := identity(a)
	if w.visited[id] {
		return
	}
	w.visited[id] = true

	llm, isLLM := reflectLLMAgent(a)
	if isLLM {
		info := AgentInfo{
			Name:        a.Name(),
			Description: a.Description(),
			Instruction: llm.instruction,
			Tools:       toolDeclarations(llm.tools, llm.toolsets),
			SubAgents:   llmSubAgents(a),
		}
		if prev, clash := w.agents[a.Name()]; clash {
			// The response is a name-keyed map, so two distinct agents sharing a
			// name cannot both be represented.
			log.Printf("appinfo: two different agents are both named %q; reporting the "+
				"first one found and dropping the other (descriptions %q and %q)",
				a.Name(), prev.Description, info.Description)
		} else {
			w.agents[a.Name()] = info
		}
	}

	for _, sub := range a.SubAgents() {
		w.walk(sub)
	}
	for _, sub := range workflowNodeAgents(a) {
		w.walk(sub)
	}
	if isLLM {
		for _, t := range allTools(llm.tools, llm.toolsets) {
			w.walk(agentBehindTool(t))
		}
	}
}

// llmSubAgents returns the LlmAgent children of a.
func llmSubAgents(a adkagent.Agent) []string {
	out := []string{}
	for _, sub := range a.SubAgents() {
		if sub == nil {
			continue
		}
		if _, isLLM := reflectLLMAgent(sub); isLLM {
			out = append(out, sub.Name())
		}
	}
	return out
}

func identity(a adkagent.Agent) any {
	v := reflect.ValueOf(a)
	if v.Kind() == reflect.Pointer {
		return v.Pointer()
	}
	if v.Type().Comparable() {
		return a
	}
	return "name:" + a.Name()
}

// llmAgentState holds the parts of an LlmAgent that ADK Go does not expose on
// the public agent.Agent interface but are read via reflection instead.
type llmAgentState struct {
	instruction string
	tools       []tool.Tool
	toolsets    []tool.Toolset
}

// reflectLLMAgent reports whether a is an ADK Go LlmAgent and, if so, extracts
// its instruction, tools and toolsets from llminternal.State.
// call.
func reflectLLMAgent(a adkagent.Agent) (llmAgentState, bool) {
	if kind, _ := agentKind(a); kind != agentKindLLM {
		return llmAgentState{}, false
	}
	// AgentType is only ever "LLMAgent" on a value llmagent.New built, which is
	// a *llmagent.llmAgent with llminternal.State embedded under the field name
	// "State".
	llmState := structOf(a).FieldByName("State")

	out := llmAgentState{}
	out.instruction, _ = fieldValue[string](llmState, "Instruction")
	out.tools, _ = fieldValue[[]tool.Tool](llmState, "Tools")
	out.toolsets, _ = fieldValue[[]tool.Toolset](llmState, "Toolsets")
	if out.instruction == "" {
		if p := llmState.FieldByName("InstructionProvider"); !p.IsNil() {
			out.instruction = fmt.Sprintf("<InstructionProvider: %s>", funcName(p))
		}
	}
	return out, true
}

// agentKindLLM is the agentinternal.Type value ADK stamps on an LlmAgent.
const agentKindLLM = "LLMAgent"

func agentKind(a adkagent.Agent) (string, bool) {
	f := structOf(a).FieldByName("AgentType")
	if !f.IsValid() {
		return "", false
	}
	return f.String(), true
}

// agentConfig reads agentinternal.State.Config, the Config the agent was
// constructed from. Returns nil for an agent.New agent, which has none.
func agentConfig(a adkagent.Agent) any {
	f := structOf(a).FieldByName("Config")
	if !f.IsValid() || f.IsNil() {
		return nil
	}
	return f.Interface()
}

// funcName returns the declared name of a function value, for reporting an
// InstructionProvider that app-info cannot evaluate.
func funcName(v reflect.Value) string {
	f := runtime.FuncForPC(v.Pointer())
	if f == nil {
		return "unknown"
	}
	name := f.Name()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// workflowNodeAgents returns the agents wrapped in a workflowagent's graph
// nodes, including nodes of nested sub-workflows.
func workflowNodeAgents(a adkagent.Agent) []adkagent.Agent {
	cfg, ok := agentConfig(a).(workflowagent.Config)
	if !ok {
		return nil
	}
	var out []adkagent.Agent
	agentsInEdges(cfg.Edges, &out, map[adkworkflow.Node]bool{})
	return out
}

// agentsInEdges collects the agents held by the nodes of an edge set.
func agentsInEdges(edges []adkworkflow.Edge, out *[]adkagent.Agent, seen map[adkworkflow.Node]bool) {
	for _, e := range edges {
		agentsInNode(e.From, out, seen)
		agentsInNode(e.To, out, seen)
	}
}

// agentsInNode collects the agents a single graph node holds.
func agentsInNode(n adkworkflow.Node, out *[]adkagent.Agent, seen map[adkworkflow.Node]bool) {
	if n == nil || seen[n] {
		return
	}
	seen[n] = true

	switch node := n.(type) {
	case *adkworkflow.AgentNode:
		// AgentNode.agent is unexported and has no accessor.
		if a, ok := readAny(field(node, "agent")).(adkagent.Agent); ok && a != nil {
			*out = append(*out, a)
		}
	case *adkworkflow.WorkflowNode:
		agentsInEdges(subWorkflowEdges(node), out, seen)
	}
}

// subWorkflowEdges returns the edges of the sub-graph nested in a WorkflowNode,
// reached through WorkflowNode.subWorkflow -> Workflow.graph -> graph.successors.
func subWorkflowEdges(n *adkworkflow.WorkflowNode) []adkworkflow.Edge {
	// Each read is guaranteed by the constructors: NewWorkflowNode builds the
	// inner Workflow and returns an error rather than a node if it cannot,
	// workflow.New always assigns a graph, and newGraph always makes the maps.
	wf, _ := readAny(field(n, "subWorkflow")).(*adkworkflow.Workflow)
	graph := readAny(field(wf, "graph"))
	successors, _ := readAny(field(graph, "successors")).(map[adkworkflow.Node][]adkworkflow.Edge)

	var edges []adkworkflow.Edge
	for _, es := range successors {
		edges = append(edges, es...)
	}
	return edges
}

// field returns the named field of the struct that container points at, or an
// invalid Value. The field may be unexported; see readAny.
func field(container any, name string) reflect.Value {
	v := reflect.ValueOf(container)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return v.FieldByName(name)
}

// agenttool.New returns an unexported *agenttool.agentTool, so it is matched by
// package path and type name rather than by a type assertion.
const (
	agentToolPkgPath  = "google.golang.org/adk/v2/tool/agenttool"
	agentToolTypeName = "agentTool"
)

// agentBehindTool returns the agent wrapped by agenttool.New, or nil for any
// other tool.
func agentBehindTool(t tool.Tool) adkagent.Agent {
	if t == nil {
		return nil
	}
	v := reflect.ValueOf(t)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct ||
		v.Type().PkgPath() != agentToolPkgPath || v.Type().Name() != agentToolTypeName {
		return nil
	}
	a, _ := readAny(v.FieldByName("agent")).(adkagent.Agent)
	return a
}

// declarer is the exported-method view of a tool that can describe itself.
// tool.Tool does not expose Declaration(), but the concrete tools returned ex. by
// functiontool.New implement Declaration() method.
type declarer interface {
	Declaration() *genai.FunctionDeclaration
}

// metadataContext is a read-only agent context used only to enumerate a
// toolset's tools for app-info. This suffices for toolsets that list tools
// without inspecting session state (e.g. MCP).
type metadataContext struct {
	context.Context
}

var _ adkagent.ReadonlyContext = metadataContext{}

func (metadataContext) UserContent() *genai.Content          { return nil }
func (metadataContext) InvocationID() string                 { return "" }
func (metadataContext) AgentName() string                    { return "" }
func (metadataContext) ReadonlyState() session.ReadonlyState { return nil }
func (metadataContext) UserID() string                       { return "" }
func (metadataContext) AppName() string                      { return "" }
func (metadataContext) SessionID() string                    { return "" }
func (metadataContext) Branch() string                       { return "" }

// allTools flattens an agent's static tools and its toolsets. A toolset that
// cannot be enumerated is skipped with a log.
func allTools(tools []tool.Tool, toolsets []tool.Toolset) []tool.Tool {
	all := append([]tool.Tool(nil), tools...)
	for _, ts := range toolsets {
		if ts == nil {
			continue
		}
		expanded, err := ts.Tools(metadataContext{Context: context.Background()})
		if err != nil {
			log.Printf("appinfo: skipping toolset %q: %v", ts.Name(), err)
			continue
		}
		all = append(all, expanded...)
	}
	return all
}

// toolDeclarations converts an agent's static tools and toolsets into the genai
// tool declarations reported by app-info. Mirrors adk-python's get_tools_info
// (https://github.com/google/adk-python/blob/main/src/google/adk/utils/agent_info.py):
// tools without a declaration are omitted.
func toolDeclarations(tools []tool.Tool, toolsets []tool.Toolset) []*genai.Tool {
	all := allTools(tools, toolsets)
	out := make([]*genai.Tool, 0, len(all))
	for _, t := range all {
		d, ok := t.(declarer)
		if !ok {
			continue
		}
		if decl := d.Declaration(); decl != nil {
			out = append(out, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{decl}})
		}
	}
	return out
}

func structOf(a adkagent.Agent) reflect.Value {
	if a == nil {
		return reflect.Value{}
	}
	v := reflect.ValueOf(a)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return v
}

// fieldValue reads exported struct field name from v and type-asserts it to T,
// returning the zero value and false if the field is absent or not a T.
func fieldValue[T any](v reflect.Value, name string) (T, bool) {
	var zero T
	f := v.FieldByName(name)
	if !f.IsValid() {
		return zero, false
	}
	t, ok := readAny(f).(T)
	return t, ok
}

// readable returns f as a value Interface() will accept, reinterpreting an
// unexported field through its own address when necessary. See readAny.
func readable(f reflect.Value) reflect.Value {
	if f.CanInterface() || !f.CanAddr() {
		return f
	}
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

// readAny reads a struct field, including an unexported one.
func readAny(f reflect.Value) any {
	if !f.IsValid() {
		return nil
	}
	f = readable(f)
	if !f.CanInterface() {
		return nil
	}
	switch f.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Slice, reflect.Map:
		if f.IsNil() {
			return nil
		}
	}
	return f.Interface()
}

// defaultPathPrefix is the prefix of the appInfo route, unless overriden by the
// `-path_prefix` flag. It defaults to "/" (the root) so callers can mount
// app-info at the root without passing the flag; normalizePathPrefix maps "/" to
// an empty prefix.
const defaultPathPrefix = "/"

// appInfoSuffix is the route appended after the path prefix to form the endpoint path.
const appInfoSuffix = "/apps/{app_name}/app-info"

// normalizePathPrefix canonicalizes a -path_prefix value into a leading-slash,
// no-trailing-slash prefix, mapping "/" (and "") to "" so the route is served at
// the root.
func normalizePathPrefix(prefix string) string {
	trimmed := strings.Trim(prefix, "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}

// launcher is a web.Sublauncher that serves the app-info route.
type appInfoLauncher struct {
	flags      *flag.FlagSet
	pathPrefix *string
}

// NewLauncher creates the app-info sublauncher, activated by the "appinfo"
// keyword. Register it before the "api" sublauncher: the api sublauncher claims
// its whole prefix with a catch-all, so this more specific route must be
// registered first to win.
func NewLauncher() weblauncher.Sublauncher {
	flags := flag.NewFlagSet("appinfo", flag.ContinueOnError)
	return &appInfoLauncher{
		flags: flags,
		pathPrefix: flags.String(
			"path_prefix",
			defaultPathPrefix,
			`URL prefix to mount app-info under; "/" serves it at the root`,
		),
	}
}

func (a *appInfoLauncher) Keyword() string { return "appinfo" }

func (a *appInfoLauncher) SimpleDescription() string {
	return "serves GET {path_prefix}/apps/{app_name}/app-info (agent metadata for eval)"
}

func (a *appInfoLauncher) CommandLineSyntax() string {
	return `  appinfo [-path_prefix <prefix>]  (prefix defaults to "/", serving at the root)`
}

// route is the fully-resolved endpoint path; only valid after Parse.
func (a *appInfoLauncher) route() string {
	return normalizePathPrefix(*a.pathPrefix) + appInfoSuffix
}

func (a *appInfoLauncher) Parse(args []string) ([]string, error) {
	if err := a.flags.Parse(args); err != nil {
		return nil, err
	}
	return a.flags.Args(), nil
}

func (a *appInfoLauncher) UserMessage(webURL string, printer func(v ...any)) {
	printer(fmt.Sprintf("       appinfo:  GET %s%s", webURL, a.route()))
}

func (a *appInfoLauncher) SetupSubrouters(router *mux.Router, config *launcher.Config) error {
	router.Methods("GET").Path(a.route()).HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			appName := mux.Vars(r)["app_name"]
			info, err := BuildAppInfo(config.AgentLoader, appName)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(info); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		})
	return nil
}
