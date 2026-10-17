package linebot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/models"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
)

// WorkflowRunner abstracts the ADK 2.0 workflow engine so the webhook handler is decoupled and easily testable.
type WorkflowRunner interface {
	HasPendingHITL(userID string) bool
	RecordUserMessage(userID, text string)
	StartIngest(ctx context.Context, userID string, imageBytes []byte, recentMessages []string) (*WorkflowResponse, error)
	ResumeHITL(ctx context.Context, userID string, userReply string) (*WorkflowResponse, error)
	RunSearch(ctx context.Context, userID string, query string) (*WorkflowResponse, error)
	RunEmailDraft(ctx context.Context, userID string, cardID string) (*WorkflowResponse, error)
}

// WorkflowResponse holds the subset of workflow execution output needed by the LINE webhook handler.
type WorkflowResponse struct {
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
}

// Handler processes incoming LINE Messaging API v8 webhook callbacks.
type Handler struct {
	cfg        *config.Config
	runner     WorkflowRunner
	apiClient  *messaging_api.MessagingApiAPI
	blobClient *messaging_api.MessagingApiBlobAPI
	async      bool
	wg         sync.WaitGroup
}

// HandlerOption configures optional Handler behavior.
type HandlerOption func(*Handler)

// WithSyncProcessing disables background goroutine dispatch per webhook request (useful in unit tests).
func WithSyncProcessing() HandlerOption {
	return func(h *Handler) {
		h.async = false
	}
}

// NewHandler creates a new LINE Bot v8 webhook handler.
func NewHandler(cfg *config.Config, runner WorkflowRunner, opts ...HandlerOption) (*Handler, error) {
	if cfg == nil {
		cfg = config.Load()
	}
	h := &Handler{
		cfg:    cfg,
		runner: runner,
		async:  true,
	}
	for _, opt := range opts {
		opt(h)
	}

	if !cfg.IsDryRunLINE() {
		apiClient, err := messaging_api.NewMessagingApiAPI(cfg.LineChannelAccessToken)
		if err != nil {
			return nil, fmt.Errorf("init LINE MessagingApiAPI: %w", err)
		}
		blobClient, err := messaging_api.NewMessagingApiBlobAPI(cfg.LineChannelAccessToken)
		if err != nil {
			return nil, fmt.Errorf("init LINE MessagingApiBlobAPI: %w", err)
		}
		h.apiClient = apiClient
		h.blobClient = blobClient
	}

	return h, nil
}

// Wait blocks until all in-flight asynchronous webhook goroutines finish.
func (h *Handler) Wait() {
	h.wg.Wait()
}

// ServeHTTP handles POST /webhook from the LINE Platform.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cb, err := h.parseWebhook(r)
	if err != nil {
		slog.Warn("failed to parse LINE webhook request", "err", err)
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Immediately return 200 OK to avoid LINE webhook timeout
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))

	if h.async {
		h.wg.Add(1)
		go func(events []webhook.EventInterface) {
			defer h.wg.Done()
			h.processEvents(context.Background(), events)
		}(cb.Events)
	} else {
		h.processEvents(r.Context(), cb.Events)
	}
}

func (h *Handler) parseWebhook(r *http.Request) (*webhook.CallbackRequest, error) {
	if !h.cfg.IsDryRunLINE() || (h.cfg.LineChannelSecret != "" && r.Header.Get("X-Line-Signature") != "") {
		return webhook.ParseRequest(h.cfg.LineChannelSecret, r)
	}

	// Dry-Run mode fallback: allow unsigned webhook payloads for local testing
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var cb webhook.CallbackRequest
	if err := json.Unmarshal(body, &cb); err != nil {
		return nil, err
	}
	return &cb, nil
}

func (h *Handler) processEvents(ctx context.Context, events []webhook.EventInterface) {
	for _, event := range events {
		switch e := event.(type) {
		case webhook.MessageEvent:
			userID := extractUserID(e.Source)
			h.showLoadingAnimation(userID)
			h.handleMessageEvent(ctx, userID, e.ReplyToken, e.Message)

		case webhook.PostbackEvent:
			userID := extractUserID(e.Source)
			h.showLoadingAnimation(userID)
			h.handlePostbackEvent(ctx, userID, e.ReplyToken, e.Postback)

		default:
			slog.Debug("ignoring unsupported LINE webhook event", "type", fmt.Sprintf("%T", event))
		}
	}
}

func (h *Handler) handleMessageEvent(ctx context.Context, userID, replyToken string, msg webhook.MessageContentInterface) {
	switch m := msg.(type) {
	case webhook.ImageMessageContent:
		imgBytes, err := h.downloadImage(m.Id)
		if err != nil {
			slog.Error("failed to download image from LINE Blob API", "message_id", m.Id, "err", err)
			h.replyText(replyToken, "⚠️ 無法讀取名片圖片，請稍後再試一次。")
			return
		}

		res, err := h.runner.StartIngest(ctx, userID, imgBytes, nil)
		if err != nil {
			slog.Error("StartIngest workflow failed", "user_id", userID, "err", err)
			h.replyText(replyToken, "⚠️ 名片辨識處理失敗，請稍後再試。")
			return
		}

		h.sendWorkflowReply(replyToken, res)

	case webhook.TextMessageContent:
		text := strings.TrimSpace(m.Text)
		if text == "" {
			return
		}

		// If the user has an active paused HITL session, route the text message into ResumeHITL
		if h.runner.HasPendingHITL(userID) {
			res, err := h.runner.ResumeHITL(ctx, userID, text)
			if err != nil {
				slog.Error("ResumeHITL workflow failed", "user_id", userID, "err", err)
				h.replyText(replyToken, "⚠️ 更新名片資訊失敗，請稍後再試。")
				return
			}
			h.sendWorkflowReply(replyToken, res)
			return
		}

		// Record recent message for context matching on subsequent card uploads, then run search
		h.runner.RecordUserMessage(userID, text)
		res, err := h.runner.RunSearch(ctx, userID, text)
		if err != nil {
			slog.Error("RunSearch workflow failed", "user_id", userID, "err", err)
			h.replyText(replyToken, "⚠️ 搜尋名片時發生錯誤，請稍後再試。")
			return
		}
		h.sendWorkflowReply(replyToken, res)

	default:
		slog.Debug("ignoring unsupported LINE message content", "type", fmt.Sprintf("%T", msg))
	}
}

func (h *Handler) handlePostbackEvent(ctx context.Context, userID, replyToken string, pb *webhook.PostbackContent) {
	if pb == nil || strings.TrimSpace(pb.Data) == "" {
		return
	}
	values, err := url.ParseQuery(pb.Data)
	if err != nil {
		slog.Warn("invalid postback query data", "data", pb.Data, "err", err)
		return
	}

	action := values.Get("action")
	cardID := values.Get("card_id")

	switch action {
	case "confirm_save", "confirm":
		res, err := h.runner.ResumeHITL(ctx, userID, pb.Data)
		if err != nil {
			slog.Error("ResumeHITL confirm failed", "user_id", userID, "err", err)
			h.replyText(replyToken, "⚠️ 確認儲存失敗（可能已儲存或工作階段已逾時）。")
			return
		}
		h.sendWorkflowReply(replyToken, res)

	case "edit", "edit_hint":
		if correction := strings.TrimSpace(values.Get("text")); correction != "" {
			res, err := h.runner.ResumeHITL(ctx, userID, correction)
			if err != nil {
				slog.Error("ResumeHITL edit failed", "user_id", userID, "err", err)
				h.replyText(replyToken, "⚠️ 修改名片資訊失敗，請稍後再試。")
				return
			}
			h.sendWorkflowReply(replyToken, res)
			return
		}
		h.replyText(replyToken, "✏️ 請直接輸入您要修改的內容（例如：「職稱改為技術副總」或「他名字是王太明，在 DevFest 認識的」）。")

	case "draft_email":
		res, err := h.runner.RunEmailDraft(ctx, userID, cardID)
		if err != nil {
			slog.Error("RunEmailDraft failed", "user_id", userID, "card_id", cardID, "err", err)
			h.replyText(replyToken, "⚠️ 無法產生破冰跟進信草稿，請確認名片是否已儲存。")
			return
		}
		h.sendWorkflowReply(replyToken, res)

	default:
		slog.Warn("unknown postback action", "action", action, "data", pb.Data)
	}
}

func (h *Handler) sendWorkflowReply(replyToken string, res *WorkflowResponse) {
	if res == nil {
		return
	}

	// 1. If paused at HITL confirmation, send the Flex Bubble Preview
	if res.WaitingHITL && res.FlexPreviewJSON != "" {
		container, err := messaging_api.UnmarshalFlexContainer([]byte(res.FlexPreviewJSON))
		if err == nil {
			altText := "名片辨識預覽"
			if res.DraftCard != nil && res.DraftCard.Name != "" {
				altText = fmt.Sprintf("名片預覽：%s (%s)", res.DraftCard.Name, res.DraftCard.Company)
			}
			flexMsg := &messaging_api.FlexMessage{
				AltText:  altText,
				Contents: container,
			}
			h.replyMessages(replyToken, []messaging_api.MessageInterface{flexMsg})
			return
		}
	}

	// 2. If search produced a Flex Carousel, send the Carousel message
	if res.FlexCarouselJSON != "" {
		container, err := messaging_api.UnmarshalFlexContainer([]byte(res.FlexCarouselJSON))
		if err == nil {
			altText := fmt.Sprintf("名片搜尋結果 (%d 筆)", len(res.SearchResults))
			flexMsg := &messaging_api.FlexMessage{
				AltText:  altText,
				Contents: container,
			}
			h.replyMessages(replyToken, []messaging_api.MessageInterface{flexMsg})
			return
		}
	}

	// 3. Otherwise send text response (saved confirmation, email draft, or empty search message)
	text := res.ReplyText
	if text == "" {
		text = res.DraftEmailText
	}
	if text != "" {
		h.replyText(replyToken, text)
	}
}

func (h *Handler) showLoadingAnimation(userID string) {
	if userID == "" {
		return
	}
	if h.apiClient == nil {
		slog.Info("[DRY-RUN LINE] ShowLoadingAnimation", "chat_id", userID, "loading_seconds", 20)
		return
	}
	_, err := h.apiClient.ShowLoadingAnimation(&messaging_api.ShowLoadingAnimationRequest{
		ChatId:         userID,
		LoadingSeconds: 20,
	})
	if err != nil {
		slog.Debug("ShowLoadingAnimation failed", "chat_id", userID, "err", err)
	}
}

func (h *Handler) downloadImage(messageID string) ([]byte, error) {
	if h.blobClient == nil {
		slog.Info("[DRY-RUN LINE] Returning mock image bytes for message", "message_id", messageID)
		return []byte("dry-run-mock-business-card-image"), nil
	}
	resp, err := h.blobClient.GetMessageContent(messageID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (h *Handler) replyText(replyToken, text string) {
	h.replyMessages(replyToken, []messaging_api.MessageInterface{
		&messaging_api.TextMessage{
			Text: text,
		},
	})
}

func (h *Handler) replyMessages(replyToken string, messages []messaging_api.MessageInterface) {
	if h.apiClient == nil || replyToken == "" {
		payload, _ := json.Marshal(messages)
		slog.Info("[DRY-RUN LINE] ReplyMessage", "reply_token", replyToken, "messages", string(payload))
		return
	}
	_, err := h.apiClient.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: replyToken,
		Messages:   messages,
	})
	if err != nil {
		slog.Error("LINE ReplyMessage failed", "reply_token", replyToken, "err", err)
	}
}

func extractUserID(src webhook.SourceInterface) string {
	switch s := src.(type) {
	case webhook.UserSource:
		return s.UserId
	case *webhook.UserSource:
		if s != nil {
			return s.UserId
		}
	case webhook.GroupSource:
		return s.UserId
	case *webhook.GroupSource:
		if s != nil {
			return s.UserId
		}
	case webhook.RoomSource:
		return s.UserId
	case *webhook.RoomSource:
		if s != nil {
			return s.UserId
		}
	}
	return ""
}
