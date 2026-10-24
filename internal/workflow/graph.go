package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/models"
	"github.com/kkdai/line-card-assistant/internal/store"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// Session state keys shared across workflow nodes.
const (
	StateKeyInputType           = "input_type"
	StateKeyUserID              = "user_id"
	StateKeyImageBytes          = "image_bytes"
	StateKeyCardImageArtifact   = "card_image_artifact"
	StateKeyCardImageVersion    = "card_image_version"
	StateKeyTextInput           = "text_input"
	StateKeyRecentMessages      = "recent_messages"
	StateKeyTargetCardID        = "target_card_id"
	StateKeyDraftCard           = "draft_card"
	StateKeyCleanPhone          = "clean_phone"
	StateKeyIsDuplicate         = "is_duplicate"
	StateKeyDuplicateOldTitle   = "duplicate_old_title"
	StateKeyDuplicateCardID     = "duplicate_card_id"
	StateKeyEnrichedCompanyInfo = "enriched_company_info"
	StateKeyEnrichedTags        = "enriched_tags"
	StateKeyEnrichedMetContext  = "enriched_met_context"
	StateKeyFlexPreviewJSON     = "flex_preview_json"
	StateKeyFlexCarouselJSON    = "flex_carousel_json"
	StateKeyDraftEmailText      = "draft_email_text"
	StateKeySearchFilter        = "search_filter"
	StateKeySearchResults       = "search_results"
	StateKeyEditInstruction     = "edit_instruction"
	StateKeyReplyText           = "reply_text"
	StateKeySavedCardID         = "saved_card_id"
	StateKeyHITLSeq             = "hitl_seq"
	StateKeyActiveInterruptID   = "active_interrupt_id"
)

const appName = "line-card-assistant"

// TurnResult captures the outputs and state snapshot after executing or resuming a workflow turn.
type TurnResult struct {
	SessionID        string
	WaitingHITL      bool
	InterruptID      string
	InterruptMessage string
	FlexPreviewJSON  string
	FlexCarouselJSON string
	DraftEmailText   string
	ReplyText        string
	SavedCardID      string
	DraftCard        *models.BusinessCard
	SearchResults    []*models.BusinessCard
	Events           []*session.Event
}

// Engine coordinates ADK 2.0 workflow execution, session persistence, artifact storage, and HITL state per LINE user.
type Engine struct {
	cfg             *config.Config
	cardStore       store.CardStore
	llm             model.LLM
	wf              *workflow.Workflow
	wfAgent         agent.Agent
	runner          *runner.Runner
	sessionService  session.Service
	artifactService artifact.Service

	mu             sync.RWMutex
	pendingSession map[string]pendingHITL // userID -> active paused HITL session info
	recentMsgs     map[string][]string    // userID -> recent chat messages for ContextMatchNode
}

type pendingHITL struct {
	SessionID   string
	InterruptID string
}

// EngineOption configures optional backend services on Engine.
type EngineOption func(*engineOptions)

type engineOptions struct {
	sessionService  session.Service
	artifactService artifact.Service
}

// WithSessionService overrides the default session.InMemoryService() on Engine.
func WithSessionService(svc session.Service) EngineOption {
	return func(o *engineOptions) {
		o.sessionService = svc
	}
}

// WithArtifactService overrides the default artifact.InMemoryService() on Engine.
func WithArtifactService(svc artifact.Service) EngineOption {
	return func(o *engineOptions) {
		o.artifactService = svc
	}
}

// NewEngine constructs the ADK 2.0 workflow engine backed by runner.Runner, session.Service, and artifact.Service.
func NewEngine(cfg *config.Config, cardStore store.CardStore, llm model.LLM, opts ...EngineOption) (*Engine, error) {
	if cfg == nil {
		cfg = config.Load()
	}
	if cardStore == nil {
		cardStore = store.NewMemoryStore()
	}
	if llm == nil {
		llm = NewMockLLM()
	}

	var eOpts engineOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&eOpts)
		}
	}

	edges, err := BuildEdges(cardStore, llm)
	if err != nil {
		return nil, fmt.Errorf("build workflow edges: %w", err)
	}

	wf, err := workflow.New("CardAssistantWorkflow", edges)
	if err != nil {
		return nil, fmt.Errorf("create workflow: %w", err)
	}

	var subAgents []agent.Agent
	if enrichAgent, err := NewCompanyEnrichAgent(llm); err == nil && enrichAgent != nil {
		subAgents = append(subAgents, enrichAgent)
	}

	wfAgent, err := workflowagent.New(workflowagent.Config{
		Name:        "CardAssistantWorkflow",
		Description: "LINE AI Business Card Assistant workflow orchestrating Vision OCR, parallel enrichment, HITL confirmation, and hybrid search.",
		Edges:       edges,
		SubAgents:   subAgents,
	})
	if err != nil {
		return nil, fmt.Errorf("create workflow agent: %w", err)
	}

	sessionService := eOpts.sessionService
	if sessionService == nil {
		sessionService = session.InMemoryService()
	}
	artifactService := eOpts.artifactService
	if artifactService == nil {
		artifactService = artifact.InMemoryService()
	}

	adkRunner, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             wfAgent,
		SessionService:    sessionService,
		ArtifactService:   artifactService,
		MemoryService:     memory.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create ADK runner: %w", err)
	}

	return &Engine{
		cfg:             cfg,
		cardStore:       cardStore,
		llm:             llm,
		wf:              wf,
		wfAgent:         wfAgent,
		runner:          adkRunner,
		sessionService:  sessionService,
		artifactService: artifactService,
		pendingSession:  make(map[string]pendingHITL),
		recentMsgs:      make(map[string][]string),
	}, nil
}

// BuildEdges constructs the full ADK 2.0 directed graph for Ingestion, Parallel Enrichment, HITL Loop, Search, and Email Drafting.
func BuildEdges(cardStore store.CardStore, llm model.LLM) ([]workflow.Edge, error) {
	if cardStore == nil {
		cardStore = store.NewMemoryStore()
	}
	if llm == nil {
		llm = NewMockLLM()
	}

	routerNode := newRouterNode()
	visionOCRNode := newVisionOCRNode(llm)
	cleanAndDedupNode := newCleanAndDedupNode(cardStore)
	companyEnrichNode := newCompanyEnrichNode(llm)
	contextMatchNode := newContextMatchNode(llm)
	joinPreviewNode := workflow.NewJoinNode(NodeJoinPreview)
	buildPreviewNode := newBuildPreviewNode()
	hitlConfirmNode := newHITLConfirmNode()
	applyEditNode := newApplyEditNode(llm, cardStore)
	saveCardNode := newSaveCardNode(cardStore, llm)

	queryIntentNode := newQueryIntentNode(llm)
	hybridSearchNode := newHybridSearchNode(cardStore)
	broadSearchNode := newBroadSearchNode(cardStore)
	formatCarouselNode := newFormatCarouselNode()
	emailDraftNode := newEmailDraftNode(llm, cardStore)

	eb := workflow.NewEdgeBuilder()

	// Entry point -> RouterNode
	eb.Add(workflow.Start, routerNode)

	// RouterNode conditional dispatch
	eb.AddRoute(routerNode, visionOCRNode, workflow.StringRoute(RouteIngest))
	eb.AddRoute(routerNode, queryIntentNode, workflow.StringRoute(RouteSearch))
	eb.AddRoute(routerNode, emailDraftNode, workflow.StringRoute(RouteEmail))

	// Flow 1: VisionOCRNode -> Parallel Fan-Out -> Fan-In Join -> BuildPreviewNode -> HITLConfirmNode
	eb.AddFanOut(visionOCRNode, cleanAndDedupNode, companyEnrichNode, contextMatchNode)
	eb.AddFanIn(joinPreviewNode, cleanAndDedupNode, companyEnrichNode, contextMatchNode)
	eb.Add(joinPreviewNode, buildPreviewNode)
	eb.Add(buildPreviewNode, hitlConfirmNode)

	// HITL Confirmation & Edit Loop
	eb.AddRoute(hitlConfirmNode, saveCardNode, workflow.StringRoute(RouteConfirm))
	eb.AddRoute(hitlConfirmNode, applyEditNode, workflow.StringRoute(RouteEdit))
	eb.AddRoute(applyEditNode, buildPreviewNode, workflow.StringRoute(RoutePreview))

	// Flow 2: Natural Language Hybrid Search & Broad Fallback
	eb.Add(queryIntentNode, hybridSearchNode)
	eb.AddRoute(hybridSearchNode, formatCarouselNode, workflow.StringRoute(RouteFound))
	eb.AddRoute(hybridSearchNode, broadSearchNode, workflow.StringRoute(RouteEmpty))
	eb.Add(broadSearchNode, formatCarouselNode)

	return eb.Build(), nil
}

// LinebotRunner returns a linebot.WorkflowRunner adapter backed by this Engine.
func (e *Engine) LinebotRunner() linebot.WorkflowRunner {
	return &linebotRunnerAdapter{engine: e}
}

type linebotRunnerAdapter struct {
	engine *Engine
}

func (a *linebotRunnerAdapter) HasPendingHITL(userID string) bool {
	return a.engine.HasPendingHITL(userID)
}

func (a *linebotRunnerAdapter) RecordUserMessage(userID, text string) {
	a.engine.RecordUserMessage(userID, text)
}

func (a *linebotRunnerAdapter) StartIngest(ctx context.Context, userID string, imageBytes []byte, recentMessages []string) (*linebot.WorkflowResponse, error) {
	res, err := a.engine.StartIngest(ctx, userID, imageBytes, recentMessages)
	if err != nil {
		return nil, err
	}
	return toWorkflowResponse(res), nil
}

func (a *linebotRunnerAdapter) ResumeHITL(ctx context.Context, userID string, userReply string) (*linebot.WorkflowResponse, error) {
	res, err := a.engine.ResumeHITL(ctx, userID, userReply)
	if err != nil {
		return nil, err
	}
	return toWorkflowResponse(res), nil
}

func (a *linebotRunnerAdapter) RunSearch(ctx context.Context, userID string, query string) (*linebot.WorkflowResponse, error) {
	res, err := a.engine.RunSearch(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	return toWorkflowResponse(res), nil
}

func (a *linebotRunnerAdapter) RunEmailDraft(ctx context.Context, userID string, cardID string) (*linebot.WorkflowResponse, error) {
	res, err := a.engine.RunEmailDraft(ctx, userID, cardID)
	if err != nil {
		return nil, err
	}
	return toWorkflowResponse(res), nil
}

func toWorkflowResponse(res *TurnResult) *linebot.WorkflowResponse {
	if res == nil {
		return nil
	}
	return &linebot.WorkflowResponse{
		SessionID:        res.SessionID,
		WaitingHITL:      res.WaitingHITL,
		InterruptID:      res.InterruptID,
		InterruptMessage: res.InterruptMessage,
		FlexPreviewJSON:  res.FlexPreviewJSON,
		FlexCarouselJSON: res.FlexCarouselJSON,
		DraftEmailText:   res.DraftEmailText,
		ReplyText:        res.ReplyText,
		SavedCardID:      res.SavedCardID,
		DraftCard:        res.DraftCard,
		SearchResults:    res.SearchResults,
	}
}

// Store returns the underlying CardStore.
func (e *Engine) Store() store.CardStore {
	return e.cardStore
}

// RecordUserMessage stores a recent user text message (up to 5 per user) for ContextMatchNode inference.
func (e *Engine) RecordUserMessage(userID, text string) {
	trimmed := strings.TrimSpace(text)
	if userID == "" || trimmed == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	msgs := append(e.recentMsgs[userID], trimmed)
	if len(msgs) > 5 {
		msgs = msgs[len(msgs)-5:]
	}
	e.recentMsgs[userID] = msgs
}

// RecentUserMessages returns a copy of the user's recent chat messages.
func (e *Engine) RecentUserMessages(userID string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	src := e.recentMsgs[userID]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// HasPendingHITL reports whether userID currently has a workflow paused at HITLConfirmNode.
func (e *Engine) HasPendingHITL(userID string) bool {
	if _, ok := e.resolvePendingHITL(context.Background(), userID); ok {
		return true
	}
	return false
}

// resolvePendingHITL looks up an active paused HITL session for userID in the local in-memory cache first,
// and falls back to scanning the backing session.Service so stateless / multi-instance deployments
// (e.g., backed by session/vertexai or Cloud SQL) can resume across instances without local memory state.
func (e *Engine) resolvePendingHITL(ctx context.Context, userID string) (pendingHITL, bool) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}
	e.mu.RLock()
	p, ok := e.pendingSession[userID]
	e.mu.RUnlock()
	if ok && p.SessionID != "" {
		if e.sessionService != nil {
			if getResp, err := e.sessionService.Get(ctx, &session.GetRequest{
				AppName:   appName,
				UserID:    userID,
				SessionID: p.SessionID,
			}); err == nil && getResp != nil && getResp.Session != nil {
				if activeID := readStringFromSession(getResp.Session, StateKeyActiveInterruptID); activeID != "" {
					found := pendingHITL{
						SessionID:   p.SessionID,
						InterruptID: activeID,
					}
					e.mu.Lock()
					e.pendingSession[userID] = found
					e.mu.Unlock()
					return found, true
				}
			}
		} else if p.InterruptID != "" {
			return p, true
		}
	}

	if e.sessionService == nil {
		return pendingHITL{}, false
	}
	listResp, err := e.sessionService.List(ctx, &session.ListRequest{
		AppName: appName,
		UserID:  userID,
	})
	if err != nil || listResp == nil {
		return pendingHITL{}, false
	}

	for _, s := range listResp.Sessions {
		if s == nil {
			continue
		}
		activeID := readStringFromSession(s, StateKeyActiveInterruptID)
		if activeID == "" {
			// Some session.Service implementations omit state maps in List() summaries; fetch full session if needed.
			if getResp, err := e.sessionService.Get(ctx, &session.GetRequest{
				AppName:   appName,
				UserID:    userID,
				SessionID: s.ID(),
			}); err == nil && getResp != nil && getResp.Session != nil {
				activeID = readStringFromSession(getResp.Session, StateKeyActiveInterruptID)
			}
		}
		if activeID != "" {
			found := pendingHITL{
				SessionID:   s.ID(),
				InterruptID: activeID,
			}
			e.mu.Lock()
			e.pendingSession[userID] = found
			e.mu.Unlock()
			return found, true
		}
	}
	return pendingHITL{}, false
}

// DefaultCardImageArtifactName is the default filename used when storing uploaded business card photos in artifact.Service.
const DefaultCardImageArtifactName = "business_card.jpg"

// StartIngest starts a new card ingestion workflow run for an uploaded business card image.
// Binary image bytes are persisted to the ADK ArtifactService (GCS in production, in-memory locally)
// and only the lightweight artifact filename + version are stored in Session.State.
func (e *Engine) StartIngest(ctx context.Context, userID string, imageBytes []byte, recentMessages []string) (*TurnResult, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}
	if len(recentMessages) == 0 {
		recentMessages = e.RecentUserMessages(userID)
	}

	sessionID := "ingest-" + uuid.NewString()
	stateDelta := map[string]any{
		StateKeyInputType:      RouteIngest,
		StateKeyUserID:         userID,
		StateKeyRecentMessages: recentMessages,
	}

	// Persist binary card photo into ADK ArtifactService instead of bloating Session.State
	if len(imageBytes) > 0 && e.artifactService != nil {
		saveResp, err := e.artifactService.Save(ctx, &artifact.SaveRequest{
			AppName:   appName,
			UserID:    userID,
			SessionID: sessionID,
			FileName:  DefaultCardImageArtifactName,
			Part:      genai.NewPartFromBytes(imageBytes, "image/jpeg"),
		})
		if err == nil && saveResp != nil {
			stateDelta[StateKeyCardImageArtifact] = DefaultCardImageArtifactName
			stateDelta[StateKeyCardImageVersion] = int(saveResp.Version)
		} else {
			// Fallback to inline bytes only if artifact save failed
			stateDelta[StateKeyImageBytes] = imageBytes
		}
	} else if len(imageBytes) > 0 {
		stateDelta[StateKeyImageBytes] = imageBytes
	}

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: RouteIngest},
		},
	}

	res, err := e.executeTurn(ctx, userID, sessionID, msg, stateDelta)
	if err != nil {
		return nil, err
	}
	e.updatePendingHITL(userID, res)
	return res, nil
}

// ResumeHITL resumes a paused card ingestion workflow with either a confirm postback ("action=confirm_save")
// or a natural-language edit instruction (e.g., "職稱改為處長").
func (e *Engine) ResumeHITL(ctx context.Context, userID string, userReply string) (*TurnResult, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}

	pending, ok := e.resolvePendingHITL(ctx, userID)
	if !ok || pending.SessionID == "" || pending.InterruptID == "" {
		return nil, fmt.Errorf("no pending HITL workflow found for user %q", userID)
	}

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					ID:   pending.InterruptID,
					Name: workflow.WorkflowInputFunctionCallName,
					Response: map[string]any{
						"response": userReply,
					},
				},
			},
		},
	}

	res, err := e.executeTurn(ctx, userID, pending.SessionID, msg, nil)
	if err != nil {
		return nil, err
	}
	e.updatePendingHITL(userID, res)
	return res, nil
}

// RunSearch executes the natural-language hybrid search workflow for userID and query.
func (e *Engine) RunSearch(ctx context.Context, userID string, query string) (*TurnResult, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}

	sessionID := "search-" + uuid.NewString()
	stateDelta := map[string]any{
		StateKeyInputType: RouteSearch,
		StateKeyUserID:    userID,
		StateKeyTextInput: query,
	}

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: query},
		},
	}

	return e.executeTurn(ctx, userID, sessionID, msg, stateDelta)
}

// RunEmailDraft executes the personalized follow-up email drafting workflow for a specific cardID.
func (e *Engine) RunEmailDraft(ctx context.Context, userID string, cardID string) (*TurnResult, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}

	sessionID := "email-" + uuid.NewString()
	stateDelta := map[string]any{
		StateKeyInputType:    RouteEmail,
		StateKeyUserID:       userID,
		StateKeyTargetCardID: cardID,
	}

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: "action=draft_email&card_id=" + cardID},
		},
	}

	return e.executeTurn(ctx, userID, sessionID, msg, stateDelta)
}

func (e *Engine) updatePendingHITL(userID string, res *TurnResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if res != nil && res.WaitingHITL && res.InterruptID != "" {
		e.pendingSession[userID] = pendingHITL{
			SessionID:   res.SessionID,
			InterruptID: res.InterruptID,
		}
	} else {
		delete(e.pendingSession, userID)
	}
}

func (e *Engine) executeTurn(ctx context.Context, userID, sessionID string, msg *genai.Content, stateDelta map[string]any) (*TurnResult, error) {
	var opts []runner.RunOption
	if len(stateDelta) > 0 {
		opts = append(opts, runner.WithStateDelta(stateDelta))
	}

	var events []*session.Event
	var waitingHITL bool
	var interruptID string
	var interruptMsg string

	for ev, err := range e.runner.Run(ctx, userID, sessionID, msg, agent.RunConfig{}, opts...) {
		if err != nil {
			return nil, err
		}
		if ev == nil {
			continue
		}
		events = append(events, ev)
		if ev.RequestedInput != nil && ev.RequestedInput.InterruptID != "" {
			waitingHITL = true
			interruptID = ev.RequestedInput.InterruptID
			interruptMsg = ev.RequestedInput.Message
		} else if len(ev.LongRunningToolIDs) > 0 {
			waitingHITL = true
			interruptID = ev.LongRunningToolIDs[0]
		}
	}

	getResp, err := e.sessionService.Get(ctx, &session.GetRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch session state after turn: %w", err)
	}

	sess := getResp.Session
	res := &TurnResult{
		SessionID:        sessionID,
		WaitingHITL:      waitingHITL,
		InterruptID:      interruptID,
		InterruptMessage: interruptMsg,
		FlexPreviewJSON:  readStringFromSession(sess, StateKeyFlexPreviewJSON),
		FlexCarouselJSON: readStringFromSession(sess, StateKeyFlexCarouselJSON),
		DraftEmailText:   readStringFromSession(sess, StateKeyDraftEmailText),
		ReplyText:        readStringFromSession(sess, StateKeyReplyText),
		SavedCardID:      readStringFromSession(sess, StateKeySavedCardID),
		DraftCard:        readDraftCardFromSession(sess),
		SearchResults:    readSearchResultsFromSession(sess),
		Events:           events,
	}
	return res, nil
}

// NewLLMModel creates either a live Gemini / Vertex AI model (via Cloud Run Self-Identity ADC or API Key)
// or a deterministic MockLLM (for Dry-Run and tests).
func NewLLMModel(ctx context.Context, cfg *config.Config) (model.LLM, error) {
	if cfg == nil || cfg.IsDryRunLLM() {
		return NewMockLLM(), nil
	}
	clientCfg := &genai.ClientConfig{
		APIKey: cfg.GoogleAPIKey,
	}
	if cfg.UseVertexAI {
		clientCfg.Backend = genai.BackendVertexAI
		if cfg.GoogleCloudProject != "" && cfg.GoogleAPIKey == "" {
			clientCfg.Project = cfg.GoogleCloudProject
			clientCfg.Location = cfg.GoogleCloudLocation
		}
	}
	return gemini.NewModel(ctx, cfg.ModelName, clientCfg)
}

// MockLLM implements model.LLM with realistic Traditional Chinese responses for Dry-Run mode and unit tests.
type MockLLM struct {
	NameValue string
}

// NewMockLLM returns a new MockLLM instance.
func NewMockLLM() *MockLLM {
	return &MockLLM{NameValue: "gemini-2.5-flash-mock"}
}

// Name returns the mock model identifier.
func (m *MockLLM) Name() string {
	if m.NameValue != "" {
		return m.NameValue
	}
	return "gemini-2.5-flash-mock"
}

// GenerateContent inspects the prompt and yields a deterministic, realistic response matching the node's expected schema.
func (m *MockLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		prompt := extractPromptText(req)
		reply := m.respondToPrompt(prompt)
		resp := &model.LLMResponse{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{Text: reply},
				},
			},
			TurnComplete: true,
		}
		yield(resp, nil)
	}
}

func (m *MockLLM) respondToPrompt(prompt string) string {
	nodePrompt := prompt
	if idx := strings.LastIndex(prompt, "[Current Node:"); idx >= 0 {
		nodePrompt = prompt[idx:]
	}
	lowerNodePrompt := strings.ToLower(nodePrompt)

	switch {
	case strings.Contains(nodePrompt, "根據使用者的修改指令更新名片 JSON"):
		if strings.Contains(prompt, "[Active Skill - hitl_multi_edit]") && strings.Contains(nodePrompt, "王大銘") {
			return `{
  "name": "王大銘 (Ming Wang)",
  "title": "研發副總",
  "company": "台灣人工智慧實驗室 (Taiwan AI Labs)",
  "email": "ming.wang@ailabs.example.tw",
  "phone": "+886-988-777-666",
  "address": "台北市大安區仁愛路三段99號",
  "website": "https://ailabs.tw",
  "company_info": "台灣人工智慧實驗室（Taiwan AI Labs）專注於醫療 AI、繁體中文大型語言模型與可信任人工智慧研發。為台灣極具代表性的前瞻 AI 研究機構。",
  "tags": ["AI", "LLM", "HealthTech", "Taiwan"],
  "met_context": "2026 Cloud Summit Taipei"
}`
		}
		return `{"status":"no_op"}`

	case strings.Contains(nodePrompt, "請辨識這張名片圖片") || strings.Contains(nodePrompt, "raw_ocr"):
		if strings.Contains(lowerNodePrompt, "jp_sony_sato") || strings.Contains(nodePrompt, "佐藤") {
			if strings.Contains(prompt, "[Active Skill - jp_card_ocr]") {
				return `{
  "name": "佐藤 健一 (Kenichi Sato)",
  "title": "車載イメージセンサ事業部 統括部長 (VP, Automotive Image Sensor Div.)",
  "company": "Sony Semiconductor Solutions (ソニーセミコンダクタソリューションズ)",
  "email": "kenichi.sato@sony.example.jp",
  "phone": "+81 (0)3-6748-2111",
  "address": "神奈川県厚木市旭町4-14-1",
  "website": "https://www.sony-semicon.com",
  "raw_ocr": "ソニーセミコンダクタソリューションズ株式会社 Sony Semiconductor Solutions\n佐藤 健一 Kenichi Sato\n車載イメージセンサ事業部 統括部長\n+81 (0)3-6748-2111\nkenichi.sato@sony.example.jp"
}`
			}
			return `{
  "name": "佐藤 健一",
  "title": "統括部長",
  "company": "ソニーセミコンダクタソリューションズ株式会社",
  "email": "kenichi.sato@sony.example.jp",
  "phone": "+81 (0)3-6748-2111",
  "address": "神奈川県厚木市旭町4-14-1",
  "website": "https://www.sony-semicon.com",
  "raw_ocr": "ソニーセミコンダクタソリューションズ株式会社\n佐藤 健一\n統括部長\n+81 (0)3-6748-2111"
}`
		}
		if strings.Contains(lowerNodePrompt, "david.chen") || strings.Contains(lowerNodePrompt, "appier") || strings.Contains(nodePrompt, "陳建宏") || strings.Contains(lowerNodePrompt, "promotion") {
			return `{
  "name": "陳建宏 (David Chen)",
  "title": "Chief Technology Officer (CTO)",
  "company": "Appier (沛星互動科技)",
  "email": "david.chen@appier.example.com",
  "phone": "0922-888-168",
  "address": "台北市信義區松仁路100號",
  "website": "https://www.appier.com",
  "raw_ocr": "Appier 沛星互動科技\n陳建宏 David Chen\nChief Technology Officer (CTO)\n0922-888-168\ndavid.chen@appier.example.com"
}`
		}
		return `{
  "name": "王大明 (Ming Wang)",
  "title": "資深技術經理",
  "company": "台灣人工智慧實驗室 (Taiwan AI Labs)",
  "email": "ming.wang@ailabs.example.tw",
  "phone": "0912-345-678",
  "address": "台北市大安區仁愛路三段99號",
  "website": "https://ailabs.tw",
  "raw_ocr": "台灣人工智慧實驗室 Taiwan AI Labs\n王大明 Ming Wang\n資深技術經理\n0912-345-678\nming.wang@ailabs.example.tw"
}`

	case strings.Contains(nodePrompt, "company_info") || strings.Contains(nodePrompt, "請查詢此公司的背景"):
		if strings.Contains(lowerNodePrompt, "sony") || strings.Contains(nodePrompt, "ソニー") {
			if strings.Contains(prompt, "[Active Skill - jp_card_ocr]") {
				return `{
  "company_info": "Sony Semiconductor Solutions（ソニーセミコンダクタソリューションズ）為全球車載與行動 CMOS 影像感測器（Image Sensor）領導廠商，提供高感度車用 ADAS 感測與邊緣 AI 晶片解決方案。",
  "tags": ["Semiconductor", "CMOS Sensor", "Automotive", "Japan"]
}`
			}
			return `{
  "company_info": "ソニーセミコンダクタソリューションズ株式会社 是專注於科技創新與數位轉型的企業，提供專業技術與產業解決方案。",
  "tags": ["AI", "Cloud", "SaaS", "Tech"]
}`
		}
		if strings.Contains(lowerNodePrompt, "appier") || strings.Contains(nodePrompt, "沛星") {
			return `{
  "company_info": "Appier（沛星互動科技）是一家以 AI 為核心的跨國 SaaS 科技公司，專注於 MarTech、AdTech 與企業級生成式 AI 決策解決方案。",
  "tags": ["AI", "MarTech", "SaaS", "Enterprise"]
}`
		}
		return `{
  "company_info": "台灣人工智慧實驗室（Taiwan AI Labs）專注於醫療 AI、繁體中文大型語言模型與可信任人工智慧研發。為台灣極具代表性的前瞻 AI 研究機構。",
  "tags": ["AI", "LLM", "HealthTech", "Taiwan"]
}`

	case strings.Contains(nodePrompt, "推斷這張名片的相遇場合"):
		lines := strings.Split(strings.TrimSpace(nodePrompt), "\n")
		lastLine := strings.TrimSpace(lines[len(lines)-1])
		if lastLine != "" && !strings.HasPrefix(lastLine, "請從以下") {
			return lastLine
		}
		return "2026 Cloud Summit Taipei"

	case strings.Contains(nodePrompt, "SearchFilter") || strings.Contains(nodePrompt, "名片搜尋語句"):
		if strings.Contains(nodePrompt, "竹科半導體大廠") || strings.Contains(lowerNodePrompt, "cowos") {
			if strings.Contains(prompt, "[Active Skill - tw_semicon_search]") {
				return `{"keywords":["台積電","TSMC"],"company":"台積電","title":"","tag":"Semiconductor"}`
			}
			return `{"keywords":[],"company":"","title":"","tag":""}`
		}
		if strings.Contains(lowerNodePrompt, "cloud summit") && strings.Contains(lowerNodePrompt, "ai") {
			return `{"keywords":["Cloud Summit","AI"],"company":"","title":"","tag":"AI"}`
		}
		if strings.Contains(lowerNodePrompt, "google") {
			return `{"keywords":["Google"],"company":"Google","title":"","tag":""}`
		}
		if strings.Contains(lowerNodePrompt, "台積電") || strings.Contains(lowerNodePrompt, "tsmc") {
			return `{"keywords":["台積電"],"company":"台灣積體電路製造","title":"","tag":"Semiconductor"}`
		}
		return `{"keywords":[],"company":"","title":"","tag":""}`

	case strings.Contains(nodePrompt, "破冰跟進信") || strings.Contains(nodePrompt, "Follow-up Email"):
		if strings.Contains(nodePrompt, "林雅婷") || strings.Contains(lowerNodePrompt, "google cloud") {
			return `主旨：很高興在 2026 Cloud Summit Taipei 認識您 — 期待與 Google Cloud Taiwan 進一步交流 AI 應用

林雅婷 (Yating Lin) Senior AI Solutions Architect 您好：

很高興日前在「2026 Cloud Summit Taipei」與您深入交流並交換名片！非常佩服 Google Cloud Taiwan 在企業雲端運算、Gemini AI 與 Vertex AI 解決方案上的卓越技術與領先實踐。

希望下週能有機會安排 15 分鐘的線上會議或咖啡交流，進一步探討雙方在 AI Agent 與雲端架構上的潛在合作機會。

祝 商祺`
		}
		if strings.Contains(nodePrompt, "王心怡") || strings.Contains(lowerNodePrompt, "cathay") {
			if strings.Contains(prompt, "[Long-Term Memory - LongTermMEMORY.md]") && strings.Contains(prompt, "FINTECH_COMPLIANCE_SUBJECT_TAG") {
				return `主旨：[FinTech 合作提案] 很高興在 FinTech Taipei 晚宴 認識您 — 期待與國泰金控 (Cathay Financial Holding) 深入交流數位金融創新

王心怡 (Cindy Wang) Partner & FinTech Lead 您好：

很高興日前在「FinTech Taipei 晚宴」與您交流並交換名片！非常佩服國泰金控 (Cathay Financial Holding) 在數位金融創新、跨場景金融生態系與 AI 風險治理上的領先佈局。

希望下週能有機會安排 15 分鐘的線上會議，進一步探討雙方在數位金融創新與企業級 AI 治理上的合作機會（本信件遵循金融合規與個資保護規範，不附載聯絡電話與地址等敏感個資）。

祝 商祺`
			}
			if strings.Contains(prompt, "[Active Skill - executive_email_drafter]") {
				return `主旨：很高興在 FinTech Taipei 晚宴 認識您 — 期待與國泰金控 (Cathay Financial Holding) 進一步交流

王心怡 (Cindy Wang) Partner & FinTech Lead 您好：

很高興日前在「FinTech Taipei 晚宴」與您深入交流並交換名片！非常佩服國泰金控 (Cathay Financial Holding) 在數位金融創新與金融科技上的卓越表現。

希望下週能有機會安排 15 分鐘的線上會議或咖啡交流，進一步探討雙方潛在的合作機會。

祝 商祺`
			}
			if strings.Contains(prompt, "RECIPIENT_PERSONALIZATION_REQUIRED") {
				return `主旨：很高興在近期活動認識您 — 期待與國泰金控進一步交流合作

王心怡 (Cindy Wang) 您好：

很高興日前與您交流並交換名片！非常佩服國泰金控 (Cathay Financial Holding) 在業界的卓越發展。

希望下週能有機會安排 15 分鐘的線上會議或咖啡交流，進一步探討雙方潛在的合作機會。

祝 商祺`
			}
		}
		if strings.Contains(nodePrompt, "佐藤 健一") || strings.Contains(lowerNodePrompt, "sony") {
			return `主旨：很高興在 2026 Cloud Summit Taipei 認識您 — 期待與 Sony Semiconductor Solutions 進一步交流車用感測與 AI 應用

佐藤 健一 (Kenichi Sato) 您好：

很高興日前在活動中與您深入交流並交換名片！非常佩服 Sony Semiconductor Solutions 在車載 CMOS 影像感測器與邊緣 AI 技術上的全球領先地位。

希望下週能有機會安排 15 分鐘的線上會議，進一步探討雙方潛在的合作機會。

祝 商祺`
		}
		if strings.Contains(nodePrompt, "王大明") || strings.Contains(nodePrompt, "王大銘") || strings.Contains(lowerNodePrompt, "taiwan ai labs") {
			recipient := "王大明 (Ming Wang)"
			if strings.Contains(nodePrompt, "王大銘") {
				recipient = "王大銘 (Ming Wang) 研發副總"
			}
			return fmt.Sprintf(`主旨：很高興在 2026 Cloud Summit Taipei 認識您 — 期待與台灣人工智慧實驗室進一步交流合作

%s 您好：

很高興日前在「2026 Cloud Summit Taipei」與您深入交流並交換名片！非常佩服台灣人工智慧實驗室 (Taiwan AI Labs) 在繁體中文大型語言模型與可信任 AI 研發上的卓越成果。

希望下週能有機會安排 15 分鐘的線上會議或咖啡交流，進一步探討雙方潛在的合作機會。

祝 商祺`, recipient)
		}
		return `主旨：很高興在活動中認識您 — 期待未來進一步交流合作

您好：

很高興日前在活動場合與您深入交流並交換名片！非常佩服貴公司在產業技術與 AI 創新應用上的卓越表現。

希望下週能有機會安排 15 分鐘的線上會議或咖啡交流，進一步探討雙方潛在的合作機會。

祝 商祺`

	default:
		return `{"status":"ok"}`
	}
}

func extractPromptText(req *model.LLMRequest) string {
	if req == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range req.Contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				sb.WriteString(p.Text)
				sb.WriteByte('\n')
			}
			if p.InlineData != nil && len(p.InlineData.Data) > 0 {
				sb.WriteString(string(p.InlineData.Data))
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// generateTextFromLLM calls llm.GenerateContent and concatenates all non-thought text parts.
func generateTextFromLLM(ctx context.Context, llm model.LLM, req *model.LLMRequest) (string, error) {
	if llm == nil {
		llm = NewMockLLM()
	}
	var sb strings.Builder
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", err
		}
		if resp == nil || resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			if part != nil && part.Text != "" && !part.Thought {
				sb.WriteString(part.Text)
			}
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// cleanJSONBlock strips optional markdown code fences (```json ... ```) around a JSON string.
func cleanJSONBlock(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			s = s[idx+1:]
		} else {
			s = strings.TrimPrefix(s, "```json")
			s = strings.TrimPrefix(s, "```")
		}
		if endIdx := strings.LastIndex(s, "```"); endIdx >= 0 {
			s = s[:endIdx]
		}
	}
	return strings.TrimSpace(s)
}

// setState records a state mutation on ev.Actions.StateDelta.
// The ADK 2.0 workflow scheduler and runner persist StateDelta on the single consumer goroutine
// before releasing the event's processed channel, ensuring thread-safe state updates across parallel branches.
func setState(_ agent.Context, ev *session.Event, key string, val any) {
	if ev == nil {
		return
	}
	if ev.Actions.StateDelta == nil {
		ev.Actions.StateDelta = make(map[string]any)
	}
	ev.Actions.StateDelta[key] = val
}

func getRawState(ctx agent.Context, key string) (any, bool) {
	if ctx == nil || ctx.Session() == nil || ctx.Session().State() == nil {
		return nil, false
	}
	val, err := ctx.Session().State().Get(key)
	if err != nil || val == nil {
		return nil, false
	}
	return val, true
}

func getStringState(ctx agent.Context, key string) string {
	val, ok := getRawState(ctx, key)
	if !ok {
		return ""
	}
	if s, ok := val.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", val)
}

func getBoolState(ctx agent.Context, key string) bool {
	val, ok := getRawState(ctx, key)
	if !ok {
		return false
	}
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

func getIntState(ctx agent.Context, key string) int {
	val, ok := getRawState(ctx, key)
	if !ok {
		return 0
	}
	switch v := val.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func getBytesState(ctx agent.Context, key string) []byte {
	val, ok := getRawState(ctx, key)
	if !ok {
		return nil
	}
	switch v := val.(type) {
	case []byte:
		return v
	case string:
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
			return decoded
		}
		return []byte(v)
	default:
		return nil
	}
}

func getStringSliceState(ctx agent.Context, key string) []string {
	val, ok := getRawState(ctx, key)
	if !ok {
		return nil
	}
	switch v := val.(type) {
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func getDraftCard(ctx agent.Context) *models.BusinessCard {
	val, ok := getRawState(ctx, keyOrDefault(StateKeyDraftCard))
	if !ok {
		return nil
	}
	return decodeBusinessCard(val)
}

func getSearchFilter(ctx agent.Context) models.SearchFilter {
	val, ok := getRawState(ctx, StateKeySearchFilter)
	if !ok {
		return models.SearchFilter{}
	}
	switch v := val.(type) {
	case models.SearchFilter:
		return v
	case *models.SearchFilter:
		if v != nil {
			return *v
		}
	default:
		b, err := json.Marshal(v)
		if err == nil {
			var f models.SearchFilter
			if json.Unmarshal(b, &f) == nil {
				return f
			}
		}
	}
	return models.SearchFilter{}
}

func getSearchResults(ctx agent.Context) []*models.BusinessCard {
	val, ok := getRawState(ctx, StateKeySearchResults)
	if !ok {
		return nil
	}
	return decodeBusinessCardSlice(val)
}

func keyOrDefault(k string) string {
	return k
}

func decodeBusinessCard(val any) *models.BusinessCard {
	switch v := val.(type) {
	case *models.BusinessCard:
		return v.Clone()
	case models.BusinessCard:
		return v.Clone()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var card models.BusinessCard
		if err := json.Unmarshal(b, &card); err != nil {
			return nil
		}
		return &card
	}
}

func decodeBusinessCardSlice(val any) []*models.BusinessCard {
	switch v := val.(type) {
	case []*models.BusinessCard:
		out := make([]*models.BusinessCard, 0, len(v))
		for _, c := range v {
			if c != nil {
				out = append(out, c.Clone())
			}
		}
		return out
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var cards []*models.BusinessCard
		if err := json.Unmarshal(b, &cards); err != nil {
			return nil
		}
		return cards
	}
}

func readStringFromSession(sess session.Session, key string) string {
	if sess == nil || sess.State() == nil {
		return ""
	}
	val, err := sess.State().Get(key)
	if err != nil || val == nil {
		return ""
	}
	if s, ok := val.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", val)
}

func readDraftCardFromSession(sess session.Session) *models.BusinessCard {
	if sess == nil || sess.State() == nil {
		return nil
	}
	val, err := sess.State().Get(StateKeyDraftCard)
	if err != nil || val == nil {
		return nil
	}
	return decodeBusinessCard(val)
}

func readSearchResultsFromSession(sess session.Session) []*models.BusinessCard {
	if sess == nil || sess.State() == nil {
		return nil
	}
	val, err := sess.State().Get(StateKeySearchResults)
	if err != nil || val == nil {
		return nil
	}
	return decodeBusinessCardSlice(val)
}
