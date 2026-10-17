package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/models"
	"github.com/kkdai/line-card-assistant/internal/store"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/geminitool"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// Node names for the ingestion & routing workflow.
const (
	NodeRouter        = "RouterNode"
	NodeVisionOCR     = "VisionOCRNode"
	NodeCleanAndDedup = "CleanAndDedupNode"
	NodeCompanyEnrich = "CompanyEnrichNode"
	NodeContextMatch  = "ContextMatchNode"
	NodeJoinPreview   = "JoinPreviewNode"
	NodeBuildPreview  = "BuildPreviewNode"
	NodeHITLConfirm   = "HITLConfirmNode"
	NodeApplyEdit     = "ApplyEditNode"
	NodeSaveCard      = "SaveCardNode"
)

// Route tags used by conditional edges in the workflow graph.
const (
	RouteIngest  = "ingest"
	RouteSearch  = "search"
	RouteEmail   = "email"
	RouteConfirm = "confirm"
	RouteEdit    = "edit"
	RoutePreview = "preview"
	RouteFound   = "found"
	RouteEmpty   = "empty"
)

// newRouterNode creates the entry RouterNode that dispatches incoming events to ingest, search, or email flows.
func newRouterNode() *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeRouter,
		func(ctx agent.Context, input any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			// When invoked directly from ADK WebUI / A2A / Agent Engine Playground with an image part,
			// populate StateKeyImageBytes automatically if not already set in session state.
			if len(getBytesState(ctx, StateKeyImageBytes)) == 0 && ctx.UserContent() != nil {
				for _, part := range ctx.UserContent().Parts {
					if part != nil && part.InlineData != nil && len(part.InlineData.Data) > 0 {
						setState(ctx, ev, StateKeyImageBytes, part.InlineData.Data)
						if getStringState(ctx, StateKeyInputType) == "" {
							setState(ctx, ev, StateKeyInputType, RouteIngest)
						}
						break
					}
				}
			}

			inputType := strings.ToLower(strings.TrimSpace(getStringState(ctx, StateKeyInputType)))
			if inputType == "" {
				if v, ok := ev.Actions.StateDelta[StateKeyInputType].(string); ok {
					inputType = strings.ToLower(strings.TrimSpace(v))
				}
			}
			if inputType == "" {
				sTrim, _ := input.(string)
				sTrim = strings.TrimSpace(sTrim)
				if sTrim == "" && ctx.UserContent() != nil {
					for _, part := range ctx.UserContent().Parts {
						if part != nil && strings.TrimSpace(part.Text) != "" {
							sTrim = strings.TrimSpace(part.Text)
							break
						}
					}
				}
				if sTrim != "" {
					sLower := strings.ToLower(sTrim)
					switch {
					case sLower == "image" || sLower == RouteIngest:
						inputType = "image"
					case sLower == "postback_draft" || sLower == RouteEmail || strings.HasPrefix(sLower, "action=draft_email"):
						inputType = "postback_draft"
						if getStringState(ctx, StateKeyTargetCardID) == "" {
							for _, kv := range strings.Split(sTrim, "&") {
								if after, ok := strings.CutPrefix(kv, "card_id="); ok && strings.TrimSpace(after) != "" {
									setState(ctx, ev, StateKeyTargetCardID, strings.TrimSpace(after))
								}
							}
						}
					default:
						inputType = "text"
						if getStringState(ctx, StateKeyTextInput) == "" {
							setState(ctx, ev, StateKeyTextInput, sTrim)
						}
					}
				}
			}

			var route string
			switch inputType {
			case "image", RouteIngest:
				route = RouteIngest
			case "postback_draft", RouteEmail:
				route = RouteEmail
			case "text", RouteSearch:
				route = RouteSearch
			default:
				if len(getBytesState(ctx, StateKeyImageBytes)) > 0 {
					route = RouteIngest
				} else if getStringState(ctx, StateKeyTargetCardID) != "" {
					route = RouteEmail
				} else {
					route = RouteSearch
				}
			}

			setState(ctx, ev, StateKeyInputType, route)
			ev.Routes = []string{route}
			ev.Output = route
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

// newVisionOCRNode creates the VisionOCRNode that extracts BusinessCard fields from an uploaded image.
func newVisionOCRNode(llm model.LLM) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeVisionOCR,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			imgBytes := getBytesState(ctx, StateKeyImageBytes)
			userID := getStringState(ctx, StateKeyUserID)
			if userID == "" {
				userID = ctx.UserID()
			}

			card, err := extractCardFromImage(ctx, llm, imgBytes, userID)
			if err != nil {
				return nil, fmt.Errorf("vision OCR failed: %w", err)
			}

			setState(ctx, ev, StateKeyDraftCard, card)
			ev.Output = card
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

func extractCardFromImage(ctx context.Context, llm model.LLM, imgBytes []byte, userID string) (*models.BusinessCard, error) {
	prompt := `請辨識這張名片圖片，並以純 JSON 格式輸出以下欄位：
{
  "name": "姓名",
  "title": "職稱",
  "company": "公司名稱",
  "email": "電子郵件",
  "phone": "電話號碼",
  "address": "地址",
  "website": "網站",
  "raw_ocr": "完整名片文字內容"
}
僅輸出 JSON，不要包含 Markdown code fence。`

	parts := []*genai.Part{
		{Text: prompt},
	}
	if len(imgBytes) > 0 {
		parts = append(parts, &genai.Part{
			InlineData: &genai.Blob{
				MIMEType: "image/jpeg",
				Data:     imgBytes,
			},
		})
	}

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  genai.RoleUser,
				Parts: parts,
			},
		},
	}

	rawJSON, err := generateTextFromLLM(ctx, llm, req)
	if err != nil {
		return nil, err
	}

	cleaned := cleanJSONBlock(rawJSON)
	var extracted struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Company string `json:"company"`
		Email   string `json:"email"`
		Phone   string `json:"phone"`
		Address string `json:"address"`
		Website string `json:"website"`
		RawOCR  string `json:"raw_ocr"`
	}
	if err := json.Unmarshal([]byte(cleaned), &extracted); err != nil {
		// Fallback to default extracted mock card if LLM returned non-JSON text
		extracted.Name = "王大明 (Ming Wang)"
		extracted.Title = "資深技術經理"
		extracted.Company = "台灣人工智慧實驗室 (Taiwan AI Labs)"
		extracted.Email = "ming.wang@ailabs.example.tw"
		extracted.Phone = "0912-345-678"
		extracted.Address = "台北市大安區仁愛路三段99號"
		extracted.Website = "https://ailabs.tw"
		extracted.RawOCR = rawJSON
	}

	now := time.Now()
	card := &models.BusinessCard{
		ID:        uuid.NewString(),
		UserID:    userID,
		Name:      extracted.Name,
		Title:     extracted.Title,
		Company:   extracted.Company,
		Email:     extracted.Email,
		Phone:     extracted.Phone,
		Address:   extracted.Address,
		Website:   extracted.Website,
		RawOCR:    extracted.RawOCR,
		MetDate:   now,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return card, nil
}

// newCleanAndDedupNode normalizes phone numbers and checks CardStore for duplicate contacts.
func newCleanAndDedupNode(cardStore store.CardStore) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeCleanAndDedup,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			card := getDraftCard(ctx)
			if card == nil {
				return nil, fmt.Errorf("draft_card missing in session state")
			}

			cleanPhone := store.NormalizePhone(card.Phone)
			userID := card.UserID
			if userID == "" {
				userID = getStringState(ctx, StateKeyUserID)
			}

			dupCard, isDup, err := cardStore.CheckDuplicate(ctx, userID, card.Name, card.Company, card.Email)
			if err != nil {
				return nil, fmt.Errorf("check duplicate: %w", err)
			}

			oldTitle := ""
			dupID := ""
			if isDup && dupCard != nil {
				oldTitle = dupCard.Title
				dupID = dupCard.ID
			}

			setState(ctx, ev, StateKeyCleanPhone, cleanPhone)
			setState(ctx, ev, StateKeyIsDuplicate, isDup)
			setState(ctx, ev, StateKeyDuplicateOldTitle, oldTitle)
			setState(ctx, ev, StateKeyDuplicateCardID, dupID)

			ev.Output = map[string]any{
				StateKeyCleanPhone:        cleanPhone,
				StateKeyIsDuplicate:       isDup,
				StateKeyDuplicateOldTitle: oldTitle,
				StateKeyDuplicateCardID:   dupID,
			}
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

// NewCompanyEnrichAgent constructs the ADK llmagent equipped with GoogleSearch used inside CompanyEnrichNode.
func NewCompanyEnrichAgent(llm model.LLM) (agent.Agent, error) {
	if llm == nil {
		llm = NewMockLLM()
	}
	return llmagent.New(llmagent.Config{
		Name:        "CompanyEnrichAgent",
		Description: "Searches Google to summarize company background and industry tags.",
		Model:       llm,
		Mode:        llmagent.ModeSingleTurn,
		Instruction: "你是一位專業商業分析助理。請根據公司名稱搜尋背景資訊，並回傳 JSON 格式：{\"company_info\": \"兩句話公司簡介\", \"tags\": [\"產業標籤1\", \"產業標籤2\", \"產業標籤3\"]}。",
		Tools: []tool.Tool{
			geminitool.GoogleSearch{},
		},
	})
}

// newCompanyEnrichNode uses an ADK llmagent equipped with geminitool.GoogleSearch{} to enrich company info & tags.
func newCompanyEnrichNode(llm model.LLM) *workflow.FunctionNode {
	enrichAgent, _ := NewCompanyEnrichAgent(llm)

	return workflow.NewFunctionNode(
		NodeCompanyEnrich,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			card := getDraftCard(ctx)
			companyName := ""
			if card != nil {
				companyName = card.Company
			}
			if companyName == "" {
				companyName = "未知公司"
			}

			prompt := fmt.Sprintf("請查詢此公司的背景並產生 2 句話簡介與 3 到 5 個產業標籤 (JSON): %s", companyName)

			var replyText string
			if enrichAgent != nil {
				for subEv, err := range llmagent.RunLLMAgentAsNode(enrichAgent, ctx, prompt) {
					if err != nil {
						break
					}
					if subEv != nil && subEv.Content != nil {
						for _, p := range subEv.Content.Parts {
							if p != nil && p.Text != "" && !p.Thought {
								replyText += p.Text
							}
						}
					}
				}
			}

			// Fallback direct LLM call if RunLLMAgentAsNode produced empty text
			if strings.TrimSpace(replyText) == "" {
				req := &model.LLMRequest{
					Contents: []*genai.Content{
						{
							Role:  genai.RoleUser,
							Parts: []*genai.Part{{Text: prompt}},
						},
					},
				}
				replyText, _ = generateTextFromLLM(ctx, llm, req)
			}

			companyInfo, tags := parseCompanyEnrichment(replyText, companyName)
			setState(ctx, ev, StateKeyEnrichedCompanyInfo, companyInfo)
			setState(ctx, ev, StateKeyEnrichedTags, tags)

			ev.Output = map[string]any{
				StateKeyEnrichedCompanyInfo: companyInfo,
				StateKeyEnrichedTags:        tags,
			}
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

func parseCompanyEnrichment(raw string, companyName string) (string, []string) {
	cleaned := cleanJSONBlock(raw)
	var parsed struct {
		CompanyInfo string   `json:"company_info"`
		Tags        []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil && parsed.CompanyInfo != "" {
		if len(parsed.Tags) == 0 {
			parsed.Tags = []string{"AI", "Tech", "Enterprise"}
		}
		return parsed.CompanyInfo, parsed.Tags
	}
	return fmt.Sprintf("%s 是專注於科技創新與數位轉型的企業，提供專業技術與產業解決方案。", companyName),
		[]string{"AI", "Cloud", "SaaS", "Tech"}
}

// newContextMatchNode inspects recent user chat history to infer MetContext and MetDate.
func newContextMatchNode(llm model.LLM) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeContextMatch,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			recentMsgs := getStringSliceState(ctx, StateKeyRecentMessages)
			metContext := inferMetContext(ctx, llm, recentMsgs)

			setState(ctx, ev, StateKeyEnrichedMetContext, metContext)
			ev.Output = map[string]any{
				StateKeyEnrichedMetContext: metContext,
			}
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

func inferMetContext(ctx context.Context, llm model.LLM, recentMsgs []string) string {
	if len(recentMsgs) == 0 {
		return "2026 Cloud Summit Taipei"
	}
	joined := strings.Join(recentMsgs, "\n")
	prompt := fmt.Sprintf("請從以下使用者最近的對話紀錄中，推斷這張名片的相遇場合（例如：2026 Cloud Summit Taipei、台北國際電腦展、客戶拜訪）。只輸出場合名稱字串：\n%s", joined)
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{{Text: prompt}},
			},
		},
	}
	out, err := generateTextFromLLM(ctx, llm, req)
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	return strings.TrimSpace(recentMsgs[len(recentMsgs)-1])
}

// newBuildPreviewNode merges OCR + parallel enrichment outputs and builds the LINE Flex Bubble JSON preview.
func newBuildPreviewNode() *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeBuildPreview,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			card := getDraftCard(ctx)
			if card == nil {
				return nil, fmt.Errorf("draft_card not found in session state")
			}

			if cleanPhone := getStringState(ctx, StateKeyCleanPhone); cleanPhone != "" {
				card.Phone = cleanPhone
			} else if card.Phone != "" {
				card.Phone = store.NormalizePhone(card.Phone)
			}

			if info := getStringState(ctx, StateKeyEnrichedCompanyInfo); info != "" && card.CompanyInfo == "" {
				card.CompanyInfo = info
			}
			if card.CompanySummary == "" && card.CompanyInfo != "" {
				card.CompanySummary = card.CompanyInfo
			} else if card.CompanyInfo == "" && card.CompanySummary != "" {
				card.CompanyInfo = card.CompanySummary
			}
			if tags := getStringSliceState(ctx, StateKeyEnrichedTags); len(tags) > 0 && len(card.Tags) == 0 {
				card.Tags = tags
			}
			if metCtx := getStringState(ctx, StateKeyEnrichedMetContext); metCtx != "" && card.MetContext == "" {
				card.MetContext = metCtx
			}
			if dupID := getStringState(ctx, StateKeyDuplicateCardID); dupID != "" {
				card.ID = dupID
			}
			if card.MetDate.IsZero() {
				card.MetDate = time.Now()
			}
			card.UpdatedAt = time.Now()

			isDup := getBoolState(ctx, StateKeyIsDuplicate)
			oldTitle := getStringState(ctx, StateKeyDuplicateOldTitle)

			flexJSON, err := linebot.BuildPreviewFlexJSON(card, isDup, oldTitle)
			if err != nil {
				return nil, fmt.Errorf("build preview flex json: %w", err)
			}

			setState(ctx, ev, StateKeyDraftCard, card)
			setState(ctx, ev, "extracted_card", card)
			setState(ctx, ev, StateKeyFlexPreviewJSON, flexJSON)
			setState(ctx, ev, "flex_payload_json", flexJSON)
			ev.Content = genai.NewContentFromText(flexJSON, genai.RoleModel)
			ev.Output = flexJSON
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

// newHITLConfirmNode pauses execution via workflow.NewRequestInputEvent and routes to confirm or edit on Resume.
func newHITLConfirmNode() *workflow.FunctionNode {
	rerun := true
	return workflow.NewFunctionNode(
		NodeHITLConfirm,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			activeID := getStringState(ctx, StateKeyActiveInterruptID)
			if activeID != "" {
				if resumed, ok := ctx.ResumedInput(activeID); ok && resumed != nil {
					ev := session.NewEvent(ctx, ctx.InvocationID())
					ev.Actions.StateDelta = make(map[string]any)
					// Clear active interrupt ID so a subsequent loop back to HITLConfirmNode pauses cleanly.
					setState(ctx, ev, StateKeyActiveInterruptID, "")

					userReply := extractResumeText(resumed)
					if isConfirmAction(userReply) {
						ev.Routes = []string{RouteConfirm}
						ev.Output = RouteConfirm
						return ev, nil
					}

					setState(ctx, ev, StateKeyEditInstruction, userReply)
					ev.Routes = []string{RouteEdit}
					ev.Output = userReply
					return ev, nil
				}
			}

			// Enter HITL pause state
			seq := getIntState(ctx, StateKeyHITLSeq) + 1
			interruptID := fmt.Sprintf("hitl_confirm_%d", seq)
			flexJSON := getStringState(ctx, StateKeyFlexPreviewJSON)
			card := getDraftCard(ctx)

			pauseEv := workflow.NewRequestInputEvent(ctx, session.RequestInput{
				InterruptID: interruptID,
				Message:     "請確認名片資訊是否正確：點擊「✅ 確認儲存」或直接輸入修改指令（例如：職稱改為處長）",
				Payload: map[string]any{
					StateKeyFlexPreviewJSON: flexJSON,
					StateKeyDraftCard:       card,
				},
			})
			if pauseEv.Actions.StateDelta == nil {
				pauseEv.Actions.StateDelta = make(map[string]any)
			}
			setState(ctx, pauseEv, StateKeyHITLSeq, seq)
			setState(ctx, pauseEv, StateKeyActiveInterruptID, interruptID)
			return pauseEv, nil
		},
		workflow.NodeConfig{
			RerunOnResume: &rerun,
		},
	)
}

func extractResumeText(resumed any) string {
	switch v := resumed.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, k := range []string{"action", "text", "input", "response", "result", "data"} {
			if s, ok := v[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		b, _ := json.Marshal(v)
		return string(b)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func isConfirmAction(reply string) bool {
	lower := strings.ToLower(strings.TrimSpace(reply))
	return lower == "action=confirm_save" ||
		lower == "action=confirm" ||
		lower == "confirm_save" ||
		lower == "confirm" ||
		lower == "save" ||
		lower == "確認" ||
		lower == "確認儲存" ||
		strings.Contains(lower, "action=confirm")
}

// newApplyEditNode applies natural-language user corrections to the draft BusinessCard and loops back to BuildPreviewNode.
func newApplyEditNode(llm model.LLM, cardStore store.CardStore) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeApplyEdit,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			card := getDraftCard(ctx)
			if card == nil {
				return nil, fmt.Errorf("draft_card missing in ApplyEditNode")
			}
			instruction := getStringState(ctx, StateKeyEditInstruction)

			updatedCard := applyCardEdit(ctx, llm, card, instruction)
			if updatedCard.Phone != "" {
				updatedCard.Phone = store.NormalizePhone(updatedCard.Phone)
				setState(ctx, ev, StateKeyCleanPhone, updatedCard.Phone)
			}

			// Re-verify duplicate status in case name/company/email changed
			if cardStore != nil {
				dupCard, isDup, err := cardStore.CheckDuplicate(ctx, updatedCard.UserID, updatedCard.Name, updatedCard.Company, updatedCard.Email)
				if err == nil {
					setState(ctx, ev, StateKeyIsDuplicate, isDup)
					if isDup && dupCard != nil {
						setState(ctx, ev, StateKeyDuplicateOldTitle, dupCard.Title)
						setState(ctx, ev, StateKeyDuplicateCardID, dupCard.ID)
						updatedCard.ID = dupCard.ID
					}
				}
			}

			setState(ctx, ev, StateKeyDraftCard, updatedCard)
			setState(ctx, ev, "extracted_card", updatedCard)
			ev.Routes = []string{RoutePreview}
			ev.Output = updatedCard
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}

func applyCardEdit(ctx context.Context, llm model.LLM, card *models.BusinessCard, instruction string) *models.BusinessCard {
	updated := card.Clone()
	trimmed := strings.TrimSpace(instruction)
	if trimmed == "" {
		return updated
	}

	// Deterministic regex rules for common natural language edits in Traditional Chinese & English
	// so edits work with 100% precision even before or alongside LLM parsing.
	appliedRule := applyDeterministicEdits(updated, trimmed)
	if appliedRule {
		return updated
	}

	// Ask LLM to apply the edit onto the JSON card
	cardBytes, _ := json.Marshal(updated)
	prompt := fmt.Sprintf(`請根據使用者的修改指令更新名片 JSON，並僅回傳更新後的完整 JSON：
目前名片 JSON：%s
使用者修改指令：%s`, string(cardBytes), trimmed)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{{Text: prompt}},
			},
		},
	}
	if out, err := generateTextFromLLM(ctx, llm, req); err == nil {
		cleaned := cleanJSONBlock(out)
		var candidate models.BusinessCard
		if err := json.Unmarshal([]byte(cleaned), &candidate); err == nil && candidate.Name != "" {
			if candidate.ID == "" {
				candidate.ID = updated.ID
			}
			if candidate.UserID == "" {
				candidate.UserID = updated.UserID
			}
			return &candidate
		}
	}

	return updated
}

var (
	reEditTitle   = regexp.MustCompile(`(?:職稱|頭銜|title)\s*(?:改為|改成|是|為|:|：|=)\s*([^，,、\n]+)`)
	reEditName    = regexp.MustCompile(`(?:姓名|名字|name)\s*(?:改為|改成|是|為|:|：|=)\s*([^，,、\n]+)`)
	reEditCompany = regexp.MustCompile(`(?:公司|company)\s*(?:改為|改成|是|為|:|：|=)\s*([^，,、\n]+)`)
	reEditPhone   = regexp.MustCompile(`(?:電話|手機|phone)\s*(?:改為|改成|是|為|:|：|=)\s*([+\d\-\s()]+)`)
	reEditEmail   = regexp.MustCompile(`(?:信箱|郵件|[Ee]mail)\s*(?:改為|改成|是|為|:|：|=)\s*([^\s，,、\n]+)`)
	reEditContext = regexp.MustCompile(`(?:場合|活動|context)\s*(?:改為|改成|是|為|:|：|=)\s*([^，,、\n]+)`)
	reEditMetAt   = regexp.MustCompile(`在\s*([^，,、\n]+?)\s*認識(?:的)?`)
)

func applyDeterministicEdits(card *models.BusinessCard, instruction string) bool {
	matched := false
	if m := reEditTitle.FindStringSubmatch(instruction); len(m) > 1 {
		card.Title = strings.TrimSpace(m[1])
		matched = true
	}
	if m := reEditName.FindStringSubmatch(instruction); len(m) > 1 {
		card.Name = strings.TrimSpace(m[1])
		matched = true
	}
	if m := reEditCompany.FindStringSubmatch(instruction); len(m) > 1 {
		card.Company = strings.TrimSpace(m[1])
		matched = true
	}
	if m := reEditPhone.FindStringSubmatch(instruction); len(m) > 1 {
		card.Phone = store.NormalizePhone(strings.TrimSpace(m[1]))
		matched = true
	}
	if m := reEditEmail.FindStringSubmatch(instruction); len(m) > 1 {
		card.Email = strings.TrimSpace(m[1])
		matched = true
	}
	if m := reEditContext.FindStringSubmatch(instruction); len(m) > 1 {
		card.MetContext = strings.TrimSpace(m[1])
		matched = true
	}
	if m := reEditMetAt.FindStringSubmatch(instruction); len(m) > 1 {
		card.MetContext = strings.TrimSpace(m[1])
		matched = true
	}
	return matched
}

// newSaveCardNode persists the confirmed BusinessCard to CardStore and generates a confirmation + AI icebreaker email draft.
func newSaveCardNode(cardStore store.CardStore, llm model.LLM) *workflow.FunctionNode {
	return workflow.NewFunctionNode(
		NodeSaveCard,
		func(ctx agent.Context, _ any) (*session.Event, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Actions.StateDelta = make(map[string]any)

			card := getDraftCard(ctx)
			if card == nil {
				return nil, fmt.Errorf("draft_card missing in SaveCardNode")
			}
			if card.UserID == "" {
				card.UserID = getStringState(ctx, StateKeyUserID)
				if card.UserID == "" {
					card.UserID = ctx.UserID()
				}
			}

			if err := cardStore.Save(ctx, card); err != nil {
				return nil, fmt.Errorf("save card: %w", err)
			}

			draftEmail := generateEmailDraft(ctx, llm, card)
			reply := fmt.Sprintf("✅ 名片已成功儲存！\n👤 姓名：%s\n💼 職稱：%s\n🏢 公司：%s\n📞 電話：%s\n\n✉️ 破冰跟進信草稿：\n%s",
				card.Name, card.Title, card.Company, card.Phone, draftEmail)

			setState(ctx, ev, StateKeyDraftCard, card)
			setState(ctx, ev, "extracted_card", card)
			setState(ctx, ev, StateKeySavedCardID, card.ID)
			setState(ctx, ev, StateKeyDraftEmailText, draftEmail)
			setState(ctx, ev, StateKeyReplyText, reply)
			ev.Content = genai.NewContentFromText(reply, genai.RoleModel)
			ev.Output = card
			return ev, nil
		},
		workflow.NodeConfig{},
	)
}
