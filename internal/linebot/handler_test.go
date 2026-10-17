package linebot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
)

func TestWebhookEndToEndDryRun(t *testing.T) {
	cfg := &config.Config{
		Port:      "8080",
		ModelName: config.DefaultModelName,
	}
	cardStore := store.NewMemoryStore()
	engine, err := workflow.NewEngine(cfg, cardStore, workflow.NewMockLLM())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	h, err := linebot.NewHandler(cfg, engine.LinebotRunner(), linebot.WithSyncProcessing())
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	userID := "U_webhook_test_1"

	// 1. Send an ImageMessageContent webhook event -> triggers StartIngest and pauses at HITL
	imgWebhook := map[string]any{
		"destination": "U_bot",
		"events": []map[string]any{
			{
				"type":       "message",
				"replyToken": "reply-token-1",
				"timestamp":  1700000000000,
				"mode":       "active",
				"source": map[string]any{
					"type":   "user",
					"userId": userID,
				},
				"message": map[string]any{
					"id":   "msg-img-1",
					"type": "image",
					"contentProvider": map[string]any{
						"type": "line",
					},
				},
			},
		},
	}
	sendWebhookRequest(t, h, imgWebhook)

	if !engine.HasPendingHITL(userID) {
		t.Fatalf("expected user %q to have pending HITL session after image upload", userID)
	}

	// 2. Send a TextMessageContent correction while HITL is paused -> triggers ResumeHITL -> ApplyEditNode -> pauses again
	editWebhook := map[string]any{
		"destination": "U_bot",
		"events": []map[string]any{
			{
				"type":       "message",
				"replyToken": "reply-token-2",
				"timestamp":  1700000001000,
				"mode":       "active",
				"source": map[string]any{
					"type":   "user",
					"userId": userID,
				},
				"message": map[string]any{
					"id":   "msg-txt-1",
					"type": "text",
					"text": "他名字是王太明，在 DevFest 認識的",
				},
			},
		},
	}
	sendWebhookRequest(t, h, editWebhook)

	if !engine.HasPendingHITL(userID) {
		t.Fatalf("expected user %q to still have pending HITL session after edit loop", userID)
	}

	// 3. Send a PostbackEvent action=confirm -> triggers ResumeHITL -> SaveCardNode
	confirmWebhook := map[string]any{
		"destination": "U_bot",
		"events": []map[string]any{
			{
				"type":       "postback",
				"replyToken": "reply-token-3",
				"timestamp":  1700000002000,
				"mode":       "active",
				"source": map[string]any{
					"type":   "user",
					"userId": userID,
				},
				"postback": map[string]any{
					"data": "action=confirm&session_id=test",
				},
			},
		},
	}
	sendWebhookRequest(t, h, confirmWebhook)

	if engine.HasPendingHITL(userID) {
		t.Fatalf("expected user %q to no longer have pending HITL after confirm postback", userID)
	}

	// Verify saved card in store has updated Name and MetContext
	cards, err := cardStore.ListByUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListByUser failed: %v", err)
	}
	foundUpdated := false
	for _, c := range cards {
		if c.Name == "王太明" && c.MetContext == "DevFest" {
			foundUpdated = true
			break
		}
	}
	if !foundUpdated {
		t.Fatalf("expected saved card with Name='王太明' and MetContext='DevFest' in store")
	}

	// 4. Send a TextMessageContent search query when no HITL is pending -> triggers RunSearch
	searchWebhook := map[string]any{
		"destination": "U_bot",
		"events": []map[string]any{
			{
				"type":       "message",
				"replyToken": "reply-token-4",
				"timestamp":  1700000003000,
				"mode":       "active",
				"source": map[string]any{
					"type":   "user",
					"userId": userID,
				},
				"message": map[string]any{
					"id":   "msg-txt-2",
					"type": "text",
					"text": "幫我找上週認識做 AI 的名片",
				},
			},
		},
	}
	sendWebhookRequest(t, h, searchWebhook)

	// 5. Send a PostbackEvent action=draft_email&card_id=card-seed-1 -> triggers RunEmailDraft
	draftWebhook := map[string]any{
		"destination": "U_bot",
		"events": []map[string]any{
			{
				"type":       "postback",
				"replyToken": "reply-token-5",
				"timestamp":  1700000004000,
				"mode":       "active",
				"source": map[string]any{
					"type":   "user",
					"userId": userID,
				},
				"postback": map[string]any{
					"data": "action=draft_email&card_id=card-seed-1",
				},
			},
		},
	}
	sendWebhookRequest(t, h, draftWebhook)
}

func sendWebhookRequest(t *testing.T, h http.Handler, payload map[string]any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d (%s)", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}
