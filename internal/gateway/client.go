// Package gateway implements the remote Agent Runtime HTTP client that decouples
// the stateless Cloud Run LINE Webhook Gateway from the stateful Google Cloud Agent Runtime
// (or ADK REST API server).
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/models"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"
	adkworkflow "google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// Client forwards LINE Bot workflow requests to a remote ADK REST API / Agent Runtime server
// over HTTP(S), implementing linebot.WorkflowRunner.
type Client struct {
	baseURL    string
	appName    string
	httpClient *http.Client

	mu             sync.RWMutex
	pendingSession map[string]pendingHITL
	recentMsgs     map[string][]string
}

type pendingHITL struct {
	SessionID   string
	InterruptID string
}

// ClientOption configures optional settings on Client.
type ClientOption func(*Client)

// WithHTTPClient overrides the underlying *http.Client used to call the Agent Runtime.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithAppName overrides the ADK application name (defaults to "app").
func WithAppName(appName string) ClientOption {
	return func(c *Client) {
		if strings.TrimSpace(appName) != "" {
			c.appName = strings.TrimSpace(appName)
		}
	}
}

// NewClient constructs a remote AgentRuntimeClient targeting baseURL (e.g. "https://my-agent-runtime.a.run.app"
// or "http://localhost:8080/api"). When useADC is true, it attaches Google Cloud Application Default Credentials
// (ID Token or OAuth2 Bearer Token) to outgoing requests.
func NewClient(ctx context.Context, baseURL string, useADC bool, opts ...ClientOption) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("agent runtime baseURL cannot be empty")
	}
	if !strings.HasSuffix(trimmed, "/api") {
		trimmed += "/api"
	}

	hc := &http.Client{Timeout: 60 * time.Second}
	if useADC {
		if idClient, err := idtoken.NewClient(ctx, strings.TrimSuffix(trimmed, "/api")); err == nil {
			hc = idClient
		} else if oauthClient, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform"); err == nil {
			hc = oauthClient
		}
	}

	c := &Client{
		baseURL:        trimmed,
		appName:        "app",
		httpClient:     hc,
		pendingSession: make(map[string]pendingHITL),
		recentMsgs:     make(map[string][]string),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c, nil
}

// RecordUserMessage caches up to 5 recent text messages per user so ContextMatchNode can infer meeting context.
func (c *Client) RecordUserMessage(userID, text string) {
	trimmed := strings.TrimSpace(text)
	if userID == "" || trimmed == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs := append(c.recentMsgs[userID], trimmed)
	if len(msgs) > 5 {
		msgs = msgs[len(msgs)-5:]
	}
	c.recentMsgs[userID] = msgs
}

// RecentUserMessages returns a copy of recent user messages for userID.
func (c *Client) RecentUserMessages(userID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	src := c.recentMsgs[userID]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// HasPendingHITL checks whether userID has a paused HITL workflow on the remote Agent Runtime.
func (c *Client) HasPendingHITL(userID string) bool {
	_, ok := c.resolvePendingHITL(context.Background(), userID)
	return ok
}

func (c *Client) resolvePendingHITL(ctx context.Context, userID string) (pendingHITL, bool) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}

	c.mu.RLock()
	cached, hasCached := c.pendingSession[userID]
	c.mu.RUnlock()
	if hasCached && cached.SessionID != "" {
		if fullSess, err := c.getSession(ctx, userID, cached.SessionID); err == nil && fullSess != nil {
			if activeID := readStringFromMap(fullSess.State, workflow.StateKeyActiveInterruptID); activeID != "" {
				found := pendingHITL{
					SessionID:   cached.SessionID,
					InterruptID: activeID,
				}
				c.mu.Lock()
				c.pendingSession[userID] = found
				c.mu.Unlock()
				return found, true
			}
		}
	}

	sessions, err := c.listSessions(ctx, userID)
	if err != nil {
		return pendingHITL{}, false
	}

	for _, sess := range sessions {
		activeID := readStringFromMap(sess.State, workflow.StateKeyActiveInterruptID)
		if activeID == "" && sess.ID != "" {
			if fullSess, err := c.getSession(ctx, userID, sess.ID); err == nil && fullSess != nil {
				activeID = readStringFromMap(fullSess.State, workflow.StateKeyActiveInterruptID)
			}
		}
		if activeID != "" {
			found := pendingHITL{
				SessionID:   sess.ID,
				InterruptID: activeID,
			}
			c.mu.Lock()
			c.pendingSession[userID] = found
			c.mu.Unlock()
			return found, true
		}
	}
	return pendingHITL{}, false
}

// StartIngest creates a remote session on the Agent Runtime and forwards the business card image
// so RouterNode saves it into ArtifactService (GCS) and executes the OCR + FanOut + HITL preview pipeline.
func (c *Client) StartIngest(ctx context.Context, userID string, imageBytes []byte, recentMessages []string) (*linebot.WorkflowResponse, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}
	if len(recentMessages) == 0 {
		recentMessages = c.RecentUserMessages(userID)
	}

	sessionID := "ingest-" + uuid.NewString()
	initialState := map[string]any{
		workflow.StateKeyInputType:      workflow.RouteIngest,
		workflow.StateKeyUserID:         userID,
		workflow.StateKeyRecentMessages: recentMessages,
	}
	if err := c.createSession(ctx, userID, sessionID, initialState); err != nil {
		return nil, err
	}

	parts := []*genai.Part{
		{Text: workflow.RouteIngest},
	}
	if len(imageBytes) > 0 {
		parts = append(parts, &genai.Part{
			InlineData: &genai.Blob{
				MIMEType: "image/jpeg",
				Data:     imageBytes,
			},
		})
	}
	msg := &genai.Content{
		Role:  genai.RoleUser,
		Parts: parts,
	}

	res, err := c.runTurn(ctx, userID, sessionID, msg, initialState)
	if err != nil {
		return nil, err
	}
	c.updatePendingHITL(userID, res)
	return res, nil
}

// ResumeHITL resumes a paused HITL session on the remote Agent Runtime using FunctionResponse.
func (c *Client) ResumeHITL(ctx context.Context, userID string, userReply string) (*linebot.WorkflowResponse, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}
	pending, ok := c.resolvePendingHITL(ctx, userID)
	if !ok || pending.SessionID == "" || pending.InterruptID == "" {
		return nil, fmt.Errorf("no pending HITL session found on remote Agent Runtime for user %q", userID)
	}

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					ID:   pending.InterruptID,
					Name: adkworkflow.WorkflowInputFunctionCallName,
					Response: map[string]any{
						"response": userReply,
					},
				},
			},
		},
	}

	res, err := c.runTurn(ctx, userID, pending.SessionID, msg, nil)
	if err != nil {
		return nil, err
	}
	c.updatePendingHITL(userID, res)
	return res, nil
}

// RunSearch executes a natural language hybrid/broad search turn on the remote Agent Runtime.
func (c *Client) RunSearch(ctx context.Context, userID string, query string) (*linebot.WorkflowResponse, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}
	sessionID := "search-" + uuid.NewString()
	initialState := map[string]any{
		workflow.StateKeyInputType: workflow.RouteSearch,
		workflow.StateKeyUserID:    userID,
		workflow.StateKeyTextInput: query,
	}
	if err := c.createSession(ctx, userID, sessionID, initialState); err != nil {
		return nil, err
	}

	msg := &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: query}},
	}
	return c.runTurn(ctx, userID, sessionID, msg, initialState)
}

// RunEmailDraft generates a personalized follow-up email for cardID on the remote Agent Runtime.
func (c *Client) RunEmailDraft(ctx context.Context, userID string, cardID string) (*linebot.WorkflowResponse, error) {
	if userID == "" {
		userID = store.DefaultDemoUserID
	}
	sessionID := "email-" + uuid.NewString()
	initialState := map[string]any{
		workflow.StateKeyInputType:    workflow.RouteEmail,
		workflow.StateKeyUserID:       userID,
		workflow.StateKeyTargetCardID: cardID,
	}
	if err := c.createSession(ctx, userID, sessionID, initialState); err != nil {
		return nil, err
	}

	msg := &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: "action=draft_email&card_id=" + cardID}},
	}
	return c.runTurn(ctx, userID, sessionID, msg, initialState)
}

func (c *Client) updatePendingHITL(userID string, res *linebot.WorkflowResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if res != nil && res.WaitingHITL && res.InterruptID != "" {
		c.pendingSession[userID] = pendingHITL{
			SessionID:   res.SessionID,
			InterruptID: res.InterruptID,
		}
	} else {
		delete(c.pendingSession, userID)
	}
}

type runAgentRequest struct {
	AppName    string         `json:"appName"`
	UserID     string         `json:"userId"`
	SessionID  string         `json:"sessionId"`
	NewMessage *genai.Content `json:"newMessage"`
	Streaming  bool           `json:"streaming"`
	StateDelta map[string]any `json:"stateDelta,omitempty"`
}

type remoteEvent struct {
	RequestedInput *struct {
		InterruptID string         `json:"interruptId"`
		Message     string         `json:"message"`
		Payload     map[string]any `json:"payload"`
	} `json:"requestedInput,omitempty"`
	LongRunningToolIDs []string `json:"longRunningToolIds,omitempty"`
}

type remoteSession struct {
	ID    string         `json:"id"`
	State map[string]any `json:"state"`
}

func (c *Client) createSession(ctx context.Context, userID, sessionID string, state map[string]any) error {
	endpoint := fmt.Sprintf(
		"%s/apps/%s/users/%s/sessions/%s",
		c.baseURL,
		url.PathEscape(c.appName),
		url.PathEscape(userID),
		url.PathEscape(sessionID),
	)
	bodyBytes, err := json.Marshal(map[string]any{
		"state": state,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("create remote session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create remote session HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func (c *Client) listSessions(ctx context.Context, userID string) ([]remoteSession, error) {
	endpoint := fmt.Sprintf(
		"%s/apps/%s/users/%s/sessions",
		c.baseURL,
		url.PathEscape(c.appName),
		url.PathEscape(userID),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list remote sessions HTTP %d", resp.StatusCode)
	}

	var sessions []remoteSession
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (c *Client) getSession(ctx context.Context, userID, sessionID string) (*remoteSession, error) {
	endpoint := fmt.Sprintf(
		"%s/apps/%s/users/%s/sessions/%s",
		c.baseURL,
		url.PathEscape(c.appName),
		url.PathEscape(userID),
		url.PathEscape(sessionID),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get remote session HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var sess remoteSession
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (c *Client) runTurn(ctx context.Context, userID, sessionID string, msg *genai.Content, stateDelta map[string]any) (*linebot.WorkflowResponse, error) {
	endpoint := c.baseURL + "/run"
	payload := runAgentRequest{
		AppName:    c.appName,
		UserID:     userID,
		SessionID:  sessionID,
		NewMessage: msg,
		Streaming:  false,
		StateDelta: stateDelta,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call remote Agent Runtime /run: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("remote Agent Runtime /run HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var events []remoteEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode remote Agent Runtime events: %w", err)
	}

	var waitingHITL bool
	var interruptID string
	var interruptMsg string
	for _, ev := range events {
		if ev.RequestedInput != nil && ev.RequestedInput.InterruptID != "" {
			waitingHITL = true
			interruptID = ev.RequestedInput.InterruptID
			interruptMsg = ev.RequestedInput.Message
		} else if len(ev.LongRunningToolIDs) > 0 {
			waitingHITL = true
			interruptID = ev.LongRunningToolIDs[0]
		}
	}

	sess, err := c.getSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}

	if !waitingHITL {
		if activeID := readStringFromMap(sess.State, workflow.StateKeyActiveInterruptID); activeID != "" {
			waitingHITL = true
			interruptID = activeID
		}
	}

	return &linebot.WorkflowResponse{
		SessionID:        sessionID,
		WaitingHITL:      waitingHITL,
		InterruptID:      interruptID,
		InterruptMessage: interruptMsg,
		FlexPreviewJSON:  readStringFromMap(sess.State, workflow.StateKeyFlexPreviewJSON),
		FlexCarouselJSON: readStringFromMap(sess.State, workflow.StateKeyFlexCarouselJSON),
		DraftEmailText:   readStringFromMap(sess.State, workflow.StateKeyDraftEmailText),
		ReplyText:        readStringFromMap(sess.State, workflow.StateKeyReplyText),
		SavedCardID:      readStringFromMap(sess.State, workflow.StateKeySavedCardID),
		DraftCard:        readCardFromMap(sess.State, workflow.StateKeyDraftCard),
		SearchResults:    readCardsSliceFromMap(sess.State, workflow.StateKeySearchResults),
	}, nil
}

func readStringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func readCardFromMap(m map[string]any, key string) *models.BusinessCard {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var c models.BusinessCard
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil
	}
	return &c
}

func readCardsSliceFromMap(m map[string]any, key string) []*models.BusinessCard {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var cards []*models.BusinessCard
	if err := json.Unmarshal(raw, &cards); err != nil {
		return nil
	}
	return cards
}
