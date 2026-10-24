package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/models"
	"github.com/kkdai/line-card-assistant/internal/store"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// Node names for the search & email drafting workflow.
const (
	NodeQueryIntent    = "QueryIntentNode"
	NodeHybridSearch   = "HybridSearchNode"
	NodeBroadSearch    = "BroadSearchNode"
	NodeFormatCarousel = "FormatCarouselNode"
	NodeEmailDraft     = "EmailDraftNode"
)

// newQueryIntentNode parses natural language search queries into a structured SearchFilter.
func newQueryIntentNode(llm model.LLM) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeQueryIntent,
		func(ctx agent.Context, _ any) (ev *session.Event, err error) {
			spanCtx, span := startNodeSpan(ctx, NodeQueryIntent, AttrInvocationID.String(ctx.InvocationID()))
			defer func() {
				endNodeSpan(span, err)
			}()

			ev = session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			query := getStringState(ctx, StateKeyTextInput)
			userID := getStringState(ctx, StateKeyUserID)
			if userID == "" {
				userID = ctx.UserID()
			}

			filter := parseSearchIntent(spanCtx, llm, userID, query)
			span.SetAttributes(AttrSearchKeywords.StringSlice(filter.Keywords))
			setState(ctx, ev, StateKeySearchFilter, filter)
			setNodeTrace(ev, NodeQueryIntent, fmt.Sprintf("keywords=%v tag=%s", filter.Keywords, filter.Tag))
			ev.Output = filter
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

func parseSearchIntent(ctx context.Context, llm model.LLM, userID, query string) models.SearchFilter {
	filter := models.SearchFilter{
		UserID: userID,
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return filter
	}

	prompt := fmt.Sprintf(`請將使用者的名片搜尋語句解析為 JSON 格式的 SearchFilter：
{
  "keywords": ["關鍵字1", "關鍵字2"],
  "company": "特定公司名稱（若無則留空）",
  "title": "特定職稱（若無則留空）",
  "tag": "產業或技術標籤（例如 AI, Cloud, FinTech, Semiconductor，若無則留空）"
}
僅輸出 JSON，不要包含 Markdown code fence。
使用者搜尋語句：%s`, trimmed)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{{Text: prompt}},
			},
		},
	}

	if raw, err := generateTextFromLLM(ctx, llm, req); err == nil {
		cleaned := cleanJSONBlock(raw)
		var parsed struct {
			Keywords []string `json:"keywords"`
			Company  string   `json:"company"`
			Title    string   `json:"title"`
			Tag      string   `json:"tag"`
		}
		if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil {
			filter.Keywords = parsed.Keywords
			filter.Company = parsed.Company
			filter.Title = parsed.Title
			filter.Tag = parsed.Tag
			if len(filter.Keywords) > 0 || filter.Company != "" || filter.Title != "" || filter.Tag != "" {
				return filter
			}
		}
	}

	// Deterministic fallback keyword extraction
	filter.Keywords = extractKeywordsHeuristic(trimmed)
	return filter
}

func extractKeywordsHeuristic(query string) []string {
	stopPhrases := []string{
		"幫我找", "請幫我找", "找一下", "請問", "有沒有", "上週在", "上禮拜在", "上次在",
		"認識做", "認識的", "認識", "做", "的人", "的名片", "名片", "聯絡人", "在", "的",
	}
	cleaned := query
	for _, sp := range stopPhrases {
		cleaned = strings.ReplaceAll(cleaned, sp, " ")
	}
	fields := strings.Fields(cleaned)
	if len(fields) == 0 {
		return []string{strings.TrimSpace(query)}
	}
	return fields
}

// newHybridSearchNode executes structured + keyword filtering via CardStore.HybridSearch.
func newHybridSearchNode(cardStore store.CardStore) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeHybridSearch,
		func(ctx agent.Context, _ any) (ev *session.Event, err error) {
			spanCtx, span := startNodeSpan(ctx, NodeHybridSearch, AttrInvocationID.String(ctx.InvocationID()))
			defer func() {
				routeAttr := ""
				if ev != nil && len(ev.Routes) > 0 {
					routeAttr = ev.Routes[0]
				}
				endNodeSpan(span, err, AttrWorkflowRoute.String(routeAttr))
			}()

			ev = session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			filter := getSearchFilter(ctx)
			if filter.UserID == "" {
				filter.UserID = getStringState(ctx, StateKeyUserID)
				if filter.UserID == "" {
					filter.UserID = ctx.UserID()
				}
			}

			cards, err := cardStore.HybridSearch(spanCtx, filter)
			if err != nil {
				return nil, fmt.Errorf("hybrid search failed: %w", err)
			}
			span.SetAttributes(AttrSearchMatches.Int(len(cards)))

			setState(ctx, ev, StateKeySearchResults, cards)
			setNodeTrace(ev, NodeHybridSearch, fmt.Sprintf("matches=%d", len(cards)))
			if len(cards) > 0 {
				ev.Routes = []string{RouteFound}
			} else {
				ev.Routes = []string{RouteEmpty}
			}
			ev.Output = cards
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

// newBroadSearchNode relaxes strict constraints and performs a fuzzy fallback search when HybridSearch returns 0 results.
func newBroadSearchNode(cardStore store.CardStore) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeBroadSearch,
		func(ctx agent.Context, _ any) (ev *session.Event, err error) {
			spanCtx, span := startNodeSpan(ctx, NodeBroadSearch, AttrInvocationID.String(ctx.InvocationID()))
			defer func() {
				endNodeSpan(span, err)
			}()

			ev = session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			filter := getSearchFilter(ctx)
			userID := filter.UserID
			if userID == "" {
				userID = getStringState(ctx, StateKeyUserID)
				if userID == "" {
					userID = ctx.UserID()
				}
			}

			keywords := append([]string{}, filter.Keywords...)
			if filter.Company != "" {
				keywords = append(keywords, filter.Company)
			}
			if filter.Title != "" {
				keywords = append(keywords, filter.Title)
			}
			if filter.Tag != "" {
				keywords = append(keywords, filter.Tag)
			}
			if rawText := getStringState(ctx, StateKeyTextInput); rawText != "" {
				keywords = append(keywords, extractKeywordsHeuristic(rawText)...)
			}

			cards, err := cardStore.BroadSearch(spanCtx, userID, keywords)
			if err != nil {
				return nil, fmt.Errorf("broad search failed: %w", err)
			}
			span.SetAttributes(AttrSearchMatches.Int(len(cards)))

			setState(ctx, ev, StateKeySearchResults, cards)
			setNodeTrace(ev, NodeBroadSearch, fmt.Sprintf("matches=%d", len(cards)))
			ev.Output = cards
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

// newFormatCarouselNode converts search results into a LINE Flex Message Carousel JSON.
func newFormatCarouselNode() *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeFormatCarousel,
		func(ctx agent.Context, _ any) (ev *session.Event, err error) {
			_, span := startNodeSpan(ctx, NodeFormatCarousel, AttrInvocationID.String(ctx.InvocationID()))
			defer func() {
				endNodeSpan(span, err)
			}()

			ev = session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			cards := getSearchResults(ctx)
			span.SetAttributes(AttrSearchMatches.Int(len(cards)))
			if len(cards) == 0 {
				reply := "😅 找不到符合條件的名片，建議換個關鍵字（例如公司名稱、職稱或活動場合）再試一次！"
				setState(ctx, ev, StateKeyReplyText, reply)
				ev.Content = genai.NewContentFromText(reply, genai.RoleModel)
				ev.Output = reply
				return ev, nil
			}

			flexJSON, err := linebot.BuildSearchCarouselFlexJSON(cards)
			if err != nil {
				return nil, fmt.Errorf("build search carousel flex json: %w", err)
			}

			reply := fmt.Sprintf("🔍 為您找到 %d 張符合條件的名片：", len(cards))
			setState(ctx, ev, StateKeyFlexCarouselJSON, flexJSON)
			setState(ctx, ev, StateKeyReplyText, reply)
			ev.Content = genai.NewContentFromText(flexJSON, genai.RoleModel)
			ev.Output = flexJSON
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

// newEmailDraftNode generates a personalized follow-up / ice-breaker email using card context and company info.
func newEmailDraftNode(llm model.LLM, cardStore store.CardStore) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeEmailDraft,
		func(ctx agent.Context, _ any) (ev *session.Event, err error) {
			spanCtx, span := startNodeSpan(ctx, NodeEmailDraft, AttrInvocationID.String(ctx.InvocationID()))
			defer func() {
				endNodeSpan(span, err)
			}()

			ev = session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			cardID := getStringState(ctx, StateKeyTargetCardID)
			var card *models.BusinessCard
			if cardID != "" && cardStore != nil {
				c, getErr := cardStore.GetByID(spanCtx, cardID)
				if getErr == nil {
					card = c
				}
			}
			if card == nil {
				card = getDraftCard(ctx)
			}
			if card == nil {
				return nil, fmt.Errorf("target business card not found for email draft (card_id=%q)", cardID)
			}
			span.SetAttributes(AttrCardID.String(card.ID), AttrCardCompany.String(card.Company))

			draft := generateEmailDraft(spanCtx, llm, card)
			setState(ctx, ev, StateKeyDraftEmailText, draft)
			setState(ctx, ev, StateKeyReplyText, draft)
			ev.Content = genai.NewContentFromText(draft, genai.RoleModel)
			ev.Output = draft
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

func generateEmailDraft(ctx context.Context, llm model.LLM, card *models.BusinessCard) string {
	prompt := fmt.Sprintf(`請為以下名片聯絡人撰寫一封專業、親切的繁體中文商務破冰跟進信（Follow-up Email），內容須自然提及相遇場合與對方公司背景：
- 姓名：%s
- 職稱：%s
- 公司：%s
- 相遇場合：%s
- 公司背景：%s`,
		card.Name, card.Title, card.Company, card.MetContext, card.CompanyInfo)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{{Text: prompt}},
			},
		},
	}

	if out, err := generateTextFromLLM(ctx, llm, req); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}

	metContext := card.MetContext
	if metContext == "" {
		metContext = "近期活動"
	}
	return fmt.Sprintf(`主旨：很高興在 %s 認識您 — 期待未來進一步交流

%s %s 您好：

很高興日前在「%s」與您交流並交換名片！非常佩服貴公司（%s）在業界的發展與創新（%s）。

希望近期能有機會安排簡短的線上會議或咖啡交流，進一步探討雙方未來潛在的合作機會。

祝 商祺`,
		metContext, card.Name, card.Title, metContext, card.Company, card.CompanyInfo)
}
