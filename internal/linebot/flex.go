package linebot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/kkdai/line-card-assistant/internal/models"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
)

// MaxCarouselBubbles is the maximum number of business card bubbles in a search carousel.
const MaxCarouselBubbles = 5

// BuildPreviewFlexJSON generates the LINE Flex Bubble JSON string for a BusinessCard preview.
func BuildPreviewFlexJSON(card *models.BusinessCard, isDup bool, oldTitle string) (string, error) {
	if card == nil {
		return "", fmt.Errorf("card cannot be nil")
	}

	headerTitle := "📇 名片辨識預覽"
	headerBg := "#1E293B"
	headerColor := "#FFFFFF"
	if isDup {
		headerTitle = "⚠️ 發現重複名片（請確認是否更新）"
		headerBg = "#FEF3C7"
		headerColor = "#92400E"
	}

	bodyContents := []map[string]any{
		{
			"type":   "text",
			"text":   nonEmpty(card.Name, "未知姓名"),
			"weight": "bold",
			"size":   "xl",
			"color":  "#0F172A",
			"wrap":   true,
		},
		{
			"type":   "text",
			"text":   nonEmpty(card.Title, "未提供職稱"),
			"size":   "sm",
			"color":  "#64748B",
			"margin": "xs",
			"wrap":   true,
		},
		{
			"type":   "text",
			"text":   nonEmpty(card.Company, "未提供公司"),
			"size":   "md",
			"weight": "bold",
			"color":  "#2563EB",
			"margin": "sm",
			"wrap":   true,
		},
	}

	if isDup {
		bannerText := "系統偵測到相同聯絡人，確認儲存將會更新既有紀錄。"
		if strings.TrimSpace(oldTitle) != "" && strings.TrimSpace(oldTitle) != strings.TrimSpace(card.Title) {
			bannerText = fmt.Sprintf("職稱異動：%s → %s", oldTitle, nonEmpty(card.Title, "未提供職稱"))
		} else if strings.TrimSpace(oldTitle) != "" {
			bannerText = fmt.Sprintf("既有職稱：%s（儲存後將合併更新）", oldTitle)
		}
		bodyContents = append(bodyContents, map[string]any{
			"type":            "box",
			"layout":          "vertical",
			"margin":          "md",
			"paddingAll":      "sm",
			"backgroundColor": "#FFFBEB",
			"cornerRadius":    "md",
			"borderWidth":     "light",
			"borderColor":     "#F59E0B",
			"contents": []map[string]any{
				{
					"type":   "text",
					"text":   bannerText,
					"size":   "xs",
					"color":  "#B45309",
					"weight": "bold",
					"wrap":   true,
				},
			},
		})
	}

	// Divider before contact details
	bodyContents = append(bodyContents, map[string]any{
		"type":   "separator",
		"margin": "md",
	})

	contactRows := []map[string]any{}
	if strings.TrimSpace(card.Email) != "" {
		contactRows = append(contactRows, buildInfoRow("Email", card.Email))
	}
	if strings.TrimSpace(card.Phone) != "" {
		contactRows = append(contactRows, buildInfoRow("電話", card.Phone))
	}
	if strings.TrimSpace(card.Address) != "" {
		contactRows = append(contactRows, buildInfoRow("地址", card.Address))
	}
	if strings.TrimSpace(card.Website) != "" {
		contactRows = append(contactRows, buildInfoRow("網站", card.Website))
	}
	if len(contactRows) == 0 {
		contactRows = append(contactRows, buildInfoRow("聯絡資訊", "未提供"))
	}

	bodyContents = append(bodyContents, map[string]any{
		"type":     "box",
		"layout":   "vertical",
		"margin":   "md",
		"spacing":  "xs",
		"contents": contactRows,
	})

	// Divider before AI Enriched Section
	bodyContents = append(bodyContents, map[string]any{
		"type":   "separator",
		"margin": "md",
	})

	// AI Enriched Section: CompanyInfo summary box, colored pill Tags, and MetContext badge
	aiSectionContents := []map[string]any{
		{
			"type":   "text",
			"text":   "✨ AI 智慧補充資訊",
			"size":   "xs",
			"weight": "bold",
			"color":  "#475569",
		},
		{
			"type":            "box",
			"layout":          "vertical",
			"margin":          "sm",
			"paddingAll":      "sm",
			"backgroundColor": "#F8FAFC",
			"cornerRadius":    "md",
			"contents": []map[string]any{
				{
					"type":  "text",
					"text":  nonEmpty(card.CompanyInfo, "尚無公司背景摘要"),
					"size":  "xs",
					"color": "#334155",
					"wrap":  true,
				},
			},
		},
	}

	if len(card.Tags) > 0 {
		aiSectionContents = append(aiSectionContents, buildTagPillsBox(card.Tags))
	}

	if strings.TrimSpace(card.MetContext) != "" {
		aiSectionContents = append(aiSectionContents, map[string]any{
			"type":            "box",
			"layout":          "horizontal",
			"margin":          "sm",
			"paddingAll":      "xs",
			"backgroundColor": "#EEF2FF",
			"cornerRadius":    "sm",
			"contents": []map[string]any{
				{
					"type":  "text",
					"text":  "📍 相遇場合：" + card.MetContext,
					"size":  "xs",
					"color": "#4338CA",
					"wrap":  true,
				},
			},
		})
	}

	bodyContents = append(bodyContents, map[string]any{
		"type":     "box",
		"layout":   "vertical",
		"margin":   "md",
		"spacing":  "xs",
		"contents": aiSectionContents,
	})

	bubble := map[string]any{
		"type": "bubble",
		"size": "mega",
		"header": map[string]any{
			"type":            "box",
			"layout":          "vertical",
			"backgroundColor": headerBg,
			"paddingAll":      "md",
			"contents": []map[string]any{
				{
					"type":   "text",
					"text":   headerTitle,
					"weight": "bold",
					"size":   "md",
					"color":  headerColor,
					"wrap":   true,
				},
			},
		},
		"body": map[string]any{
			"type":     "box",
			"layout":   "vertical",
			"contents": bodyContents,
		},
		"footer": map[string]any{
			"type":    "box",
			"layout":  "vertical",
			"spacing": "sm",
			"contents": []map[string]any{
				{
					"type":   "button",
					"style":  "primary",
					"color":  "#16A34A",
					"height": "sm",
					"action": map[string]any{
						"type":        "postback",
						"label":       "✅ 確認存檔",
						"data":        "action=confirm_save",
						"displayText": "✅ 確認儲存名片",
					},
				},
				{
					"type":   "button",
					"style":  "secondary",
					"height": "sm",
					"action": map[string]any{
						"type":        "postback",
						"label":       "✏️ 修改欄位 / 補備註",
						"data":        "action=edit",
						"inputOption": "openKeyboard",
						"fillInText":  "職稱改為：\n場合改為：",
					},
				},
				{
					"type":   "text",
					"text":   "💡 點擊上方按鈕或直接輸入修改指令（例如：職稱改為處長）",
					"size":   "xxs",
					"color":  "#64748B",
					"align":  "center",
					"margin": "sm",
					"wrap":   true,
				},
			},
		},
	}

	raw, err := json.Marshal(bubble)
	if err != nil {
		return "", fmt.Errorf("marshal preview flex bubble: %w", err)
	}
	return string(raw), nil
}

// BuildPreviewFlexMessage builds both the LINE SDK FlexMessage and raw JSON string for a BusinessCard preview.
func BuildPreviewFlexMessage(card *models.BusinessCard, isDup bool, oldTitle string) (*messaging_api.FlexMessage, string, error) {
	jsonStr, err := BuildPreviewFlexJSON(card, isDup, oldTitle)
	if err != nil {
		return nil, "", err
	}
	container, err := messaging_api.UnmarshalFlexContainer([]byte(jsonStr))
	if err != nil {
		return nil, jsonStr, fmt.Errorf("unmarshal flex container: %w", err)
	}
	altText := fmt.Sprintf("名片預覽：%s (%s)", nonEmpty(card.Name, "未知"), nonEmpty(card.Company, "未知公司"))
	msg := &messaging_api.FlexMessage{
		AltText:  altText,
		Contents: container,
	}
	return msg, jsonStr, nil
}

// BuildSearchCarouselFlexJSON generates a LINE Flex Carousel JSON string (up to 5 bubbles) for search results.
func BuildSearchCarouselFlexJSON(cards []*models.BusinessCard) (string, error) {
	limit := len(cards)
	if limit > MaxCarouselBubbles {
		limit = MaxCarouselBubbles
	}

	bubbles := make([]map[string]any, 0, limit)
	for i := 0; i < limit; i++ {
		c := cards[i]
		if c == nil {
			continue
		}
		bubbles = append(bubbles, buildSearchBubbleMap(c))
	}

	carousel := map[string]any{
		"type":     "carousel",
		"contents": bubbles,
	}
	raw, err := json.Marshal(carousel)
	if err != nil {
		return "", fmt.Errorf("marshal search carousel: %w", err)
	}
	return string(raw), nil
}

// BuildSearchCarouselFlexMessage builds both the LINE SDK FlexMessage and raw JSON string for search results.
func BuildSearchCarouselFlexMessage(cards []*models.BusinessCard) (*messaging_api.FlexMessage, string, error) {
	jsonStr, err := BuildSearchCarouselFlexJSON(cards)
	if err != nil {
		return nil, "", err
	}
	container, err := messaging_api.UnmarshalFlexContainer([]byte(jsonStr))
	if err != nil {
		return nil, jsonStr, fmt.Errorf("unmarshal search carousel container: %w", err)
	}
	msg := &messaging_api.FlexMessage{
		AltText:  fmt.Sprintf("找到 %d 張符合的名片", len(cards)),
		Contents: container,
	}
	return msg, jsonStr, nil
}

func buildSearchBubbleMap(c *models.BusinessCard) map[string]any {
	bodyContents := []map[string]any{
		{
			"type":   "text",
			"text":   nonEmpty(c.Name, "未知姓名"),
			"weight": "bold",
			"size":   "lg",
			"color":  "#0F172A",
			"wrap":   true,
		},
		{
			"type":   "text",
			"text":   nonEmpty(c.Title, "未提供職稱"),
			"size":   "xs",
			"color":  "#64748B",
			"margin": "xs",
			"wrap":   true,
		},
		{
			"type":   "text",
			"text":   nonEmpty(c.Company, "未提供公司"),
			"size":   "sm",
			"weight": "bold",
			"color":  "#2563EB",
			"margin": "sm",
			"wrap":   true,
		},
	}

	if len(c.Tags) > 0 {
		bodyContents = append(bodyContents, buildTagPillsBox(c.Tags))
	}

	if strings.TrimSpace(c.MetContext) != "" {
		bodyContents = append(bodyContents, map[string]any{
			"type":   "text",
			"text":   "📍 " + c.MetContext,
			"size":   "xs",
			"color":  "#4338CA",
			"margin": "md",
			"wrap":   true,
		})
	}

	if strings.TrimSpace(c.CompanyInfo) != "" {
		bodyContents = append(bodyContents, map[string]any{
			"type":     "text",
			"text":     c.CompanyInfo,
			"size":     "xxs",
			"color":    "#475569",
			"margin":   "sm",
			"wrap":     true,
			"maxLines": 3,
		})
	}

	telURI := sanitizeTelURI(c.Phone)
	mailtoURI := sanitizeMailtoURI(c.Email)
	cardID := nonEmpty(c.ID, "unknown")

	footerButtons := []map[string]any{
		{
			"type":   "button",
			"style":  "secondary",
			"height": "sm",
			"action": map[string]any{
				"type":  "uri",
				"label": "📞 撥打電話",
				"uri":   telURI,
			},
		},
		{
			"type":   "button",
			"style":  "secondary",
			"height": "sm",
			"action": map[string]any{
				"type":  "uri",
				"label": "✉️ 寄送 Email",
				"uri":   mailtoURI,
			},
		},
		{
			"type":   "button",
			"style":  "primary",
			"color":  "#2563EB",
			"height": "sm",
			"action": map[string]any{
				"type":        "postback",
				"label":       "🤖 撰寫破冰信",
				"data":        fmt.Sprintf("action=draft_email&card_id=%s", cardID),
				"displayText": fmt.Sprintf("請幫我撰寫給 %s 的破冰信", nonEmpty(c.Name, "對方")),
			},
		},
	}

	return map[string]any{
		"type": "bubble",
		"size": "kilo",
		"body": map[string]any{
			"type":     "box",
			"layout":   "vertical",
			"contents": bodyContents,
		},
		"footer": map[string]any{
			"type":     "box",
			"layout":   "vertical",
			"spacing":  "xs",
			"contents": footerButtons,
		},
	}
}

func buildInfoRow(label, value string) map[string]any {
	return map[string]any{
		"type":    "box",
		"layout":  "baseline",
		"spacing": "sm",
		"contents": []map[string]any{
			{
				"type":  "text",
				"text":  label,
				"color": "#94A3B8",
				"size":  "xs",
				"flex":  2,
			},
			{
				"type":  "text",
				"text":  value,
				"color": "#334155",
				"size":  "xs",
				"flex":  5,
				"wrap":  true,
			},
		},
	}
}

func buildTagPillsBox(tags []string) map[string]any {
	limit := len(tags)
	if limit > 4 {
		limit = 4
	}
	pills := make([]map[string]any, 0, limit)
	for i := 0; i < limit; i++ {
		tag := strings.TrimSpace(tags[i])
		if tag == "" {
			continue
		}
		pills = append(pills, map[string]any{
			"type":            "box",
			"layout":          "vertical",
			"backgroundColor": "#DBEAFE",
			"cornerRadius":    "md",
			"paddingAll":      "xs",
			"contents": []map[string]any{
				{
					"type":  "text",
					"text":  "#" + tag,
					"size":  "xxs",
					"color": "#1D4ED8",
					"align": "center",
				},
			},
		})
	}
	return map[string]any{
		"type":     "box",
		"layout":   "horizontal",
		"margin":   "sm",
		"spacing":  "xs",
		"contents": pills,
	}
}

func sanitizeTelURI(phone string) string {
	cleaned := regexp.MustCompile(`[^\d+]`).ReplaceAllString(strings.TrimSpace(phone), "")
	if cleaned == "" {
		return "tel:+886900000000"
	}
	return "tel:" + cleaned
}

func sanitizeMailtoURI(email string) string {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" || !strings.Contains(trimmed, "@") {
		return "mailto:contact@example.com"
	}
	return "mailto:" + trimmed
}

func nonEmpty(val, fallback string) string {
	if strings.TrimSpace(val) == "" {
		return fallback
	}
	return val
}
