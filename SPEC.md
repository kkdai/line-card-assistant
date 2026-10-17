# Role & Objective
You are an autonomous principal Go engineer. Your goal is to build a complete, compilable, and tested **LINE Bot Business Card Assistant (`line-card-assistant`)** in Go (`1.23+`), powered by **Google ADK 2.0 Graph Workflows (`google.golang.org/adk/v2`)**, **Gemini 3.8 Flash**, and **LINE Messaging API SDK v8**.

Execute this specification end-to-end without stopping for human confirmation. Create all files, resolve dependencies, implement mock-friendly interfaces for external DB/APIs, and verify everything passes `go build ./...` and `go test ./...`.

---

## 1. Tech Stack & Dependencies
- **Language**: Go 1.23+
- **AI & Agent Framework**:
  - Google ADK 2.0 (`google.golang.org/adk/v2`, `google.golang.org/adk/v2/workflow`)
  - Google GenAI SDK (`google.golang.org/genai`) — Model: `gemini-3.8-flash`
- **LINE Bot SDK**: `github.com/line/line-bot-sdk-go/v8/linebot/messaging_api` & `webhook`
- **Storage & Session**:
  - In-Memory / SQLite dual-mode repository interface (default to in-memory with pre-seeded mock data so tests and local dev run out-of-the-box without external DB setup).
  - ADK Session Store for persisting paused Graph states (`waiting` state for Human-in-the-Loop).

---

## 2. Project Directory Structure
Create the following exact project layout:
```text
.
├── cmd/
│   └── server/
│       └── main.go                 # HTTP server entrypoint (/webhook, /healthz)
├── internal/
│   ├── config/
│   │   └── config.go               # Env var loader (PORT, LINE keys, GOOGLE_API_KEY)
│   ├── models/
│   │   └── card.go                 # BusinessCard struct, Graph State struct
│   ├── store/
│   │   ├── store.go                # CardRepository interface (Save, CheckDuplicate, HybridSearch)
│   │   └── memory.go               # Thread-safe in-memory implementation with seed data
│   ├── workflow/
│   │   ├── graph.go                # ADK 2.0 Graph Workflow builder & runner
│   │   ├── nodes_ingest.go         # Path A nodes: VisionOCR, CleanAndDedup, CompanyEnrich, ContextMatch, JoinPreview, HITLPause, CommitCard
│   │   └── nodes_search.go         # Path B nodes: QueryAnalyzer, HybridSearch, RenderCarousel
│   └── linebot/
│       ├── handler.go              # LINE Webhook event router (Image, Text, Postback)
│       └── flex.go                 # Flex Message JSON builders (Preview Card & Search Carousel)
├── Dockerfile                      # Multi-stage Cloud Run ready Dockerfile
├── Makefile                        # build, run, test targets
└── README.md                       # Architecture diagram (Mermaid) & quickstart guide
```

---

## 3. Core Data Models (`internal/models/card.go`)

```go
package models

import "time"

type BusinessCard struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Title         string    `json:"title"`
	Company       string    `json:"company"`
	Email         string    `json:"email"`
	Phone         string    `json:"phone"`          // Normalized format: +886-9xx-xxx-xxx
	Address       string    `json:"address"`
	TaxID         string    `json:"tax_id"`
	CompanySummary string   `json:"company_summary"` // Enriched by Google Search Grounding
	MetContext    string    `json:"met_context"`     // e.g., "2026 Google Cloud Summit Taipei"
	RawImageURL   string    `json:"raw_image_url"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// CardWorkflowState binds to ADK 2.0 Session State using state tags
type CardWorkflowState struct {
	UserID          string         `state:"user_id" json:"user_id"`
	InputType       string         `state:"input_type" json:"input_type"` // "image", "text", "postback_confirm", "postback_edit"
	ImageBytes      []byte         `state:"image_bytes" json:"-"`
	RawTextQuery    string         `state:"raw_text_query" json:"raw_text_query"`
	ExtractedCard   *BusinessCard  `state:"extracted_card" json:"extracted_card"`
	DuplicateRecord *BusinessCard  `state:"duplicate_record" json:"duplicate_record"`
	IsDuplicate     bool           `state:"is_duplicate" json:"is_duplicate"`
	SearchResults   []BusinessCard `state:"search_results" json:"search_results"`
	FlexPayloadJSON string         `state:"flex_payload_json" json:"flex_payload_json"`
	UserDecision    string         `state:"user_decision" json:"user_decision"` // "confirm", "edit", "cancel"
	UserCorrection  string         `state:"user_correction" json:"user_correction"`
}
```

---

## 4. ADK 2.0 Graph Workflow Specification (`internal/workflow/`)

Design the workflow graph with two main branches routed by `InputType`:

### Branch A: Card Ingestion Flow (`InputType == "image"`)
1. **`VisionOCRNode` (`LlmAgent` / `gemini-3.8-flash`)**:
   - Extracts structured JSON (`Name`, `Title`, `Company`, `Email`, `Phone`, `Address`, `TaxID`) from `ImageBytes`.
2. **Parallel Fan-Out (`AddFanOut`)** -> Triggers 3 concurrent nodes:
   - **`CleanAndDedupNode` (`NewFunctionNode`)**: Deterministic Go logic (NO LLM). Formats phone numbers via regex (`09\d{2}-?\d{3}-?\d{3}` -> `+886-9xx-xxx-xxx`), validates email syntax, and queries `CardRepository.CheckDuplicate(name, email)`. If found, populates `DuplicateRecord` and `IsDuplicate = true`.
   - **`CompanyEnrichNode` (`LlmAgent` with Google Search Grounding)**: Searches `ExtractedCard.Company` and generates a 30-word Traditional Chinese summary (`CompanySummary`) of the company's core business.
   - **`ContextMatchNode` (`NewFunctionNode`)**: Tags `MetContext` with current date and location/event context.
3. **Fan-In Join (`AddFanIn` -> `BuildPreviewNode`)**:
   - Merges outputs from the 3 parallel nodes and builds a LINE Flex Message preview bubble.
   - If `IsDuplicate == true`, adds a warning banner in the Flex Message showing the previous job title vs. new job title.
4. **Human-in-the-Loop Pause (`HITLConfirmNode` using `NewRequestInputEvent`)**:
   - Emits a request input event and pauses the Graph execution in `waiting` state, returning the Flex Message to the user.
   - **Resume Logic**:
     - When LINE Webhook receives `action=confirm&session_id=xxx`, resume graph -> transition to **`CommitCardNode`** (saves to DB and returns confirmation text + AI-generated icebreaker email draft).
     - When LINE Webhook receives `action=edit&session_id=xxx` followed by user correction text (e.g., "他名字是王太明，在 DevFest 認識的"), resume graph -> route to **`ApplyEditNode`** (`LlmAgent` updates `ExtractedCard` fields) -> loop back to `BuildPreviewNode` and pause again.

### Branch B: Semantic & Hybrid Search Flow (`InputType == "text"`)
1. **`QueryAnalyzerNode` (`LlmAgent`)**:
   - Parses natural language queries (e.g., "幫我找上次在研討會遇到做 AI 的技術主管") into structured filter criteria (`Keywords`, `RoleFilter`, `ContextFilter`).
2. **`HybridSearchNode` (`NewFunctionNode`)**:
   - Queries `CardRepository.HybridSearch()`. If strict match returns 0 results, automatically executes fallback fuzzy matching.
3. **`RenderCarouselNode` (`NewFunctionNode`)**:
   - Generates a LINE Flex Message Carousel (up to 5 bubbles) with `URIAction` (`tel:`, `mailto:`) and `PostbackAction` (`action=draft_email&card_id=xxx`).

---

## 5. LINE Bot Integration Details (`internal/linebot/`)
- **Loading Animation**: Call LINE `ShowLoadingAnimation` API immediately upon receiving an image or search query before invoking the Graph Workflow.
- **Postback Payload Format**:
  - Confirm Save: `action=confirm&session_id=<uuid>`
  - Request Edit: `action=edit&session_id=<uuid>`
  - Draft Follow-up Email: `action=draft_email&card_id=<id>`
- **Graceful Fallback**: If `GOOGLE_API_KEY` or `LINE_CHANNEL_ACCESS_TOKEN` is missing during local testing, run in **Dry-Run / Mock LLM Mode** so unit tests and webhook integration tests can execute deterministically.

---

## 6. Execution & Verification Steps (Self-Validation)
Perform these steps sequentially:
1. Initialize `go.mod` (`module github.com/kkdai/line-card-assistant`).
2. Implement all packages under `internal/` and `cmd/server/`.
3. Write comprehensive unit and workflow tests in `internal/workflow/graph_test.go`:
   - **Test 1 (`TestCardIngestAndHITLResume`)**: Simulate uploading a card image -> verify Fan-Out nodes populate `Phone` normalization, `IsDuplicate`, and `CompanySummary` -> verify Graph pauses at HITL -> simulate a Postback `confirm` resume -> verify card is committed to `memory.Store`.
   - **Test 2 (`TestCardIngestEditLoop`)**: Simulate HITL pause -> simulate user sending `edit` correction -> verify loop updates `ExtractedCard` and re-emits Flex preview.
   - **Test 3 (`TestHybridSearchFlow`)**: Query pre-seeded cards by company/context and verify Flex Carousel JSON output.
4. Run `gofmt -s -w .`
5. Run `go build ./...` and `go test -v ./...` and fix any compilation or test errors until 100% green.