# LINE Card Assistant (`line-card-assistant`)

An intelligent **LINE Bot Business Card Assistant** written in Go, powered by **Google ADK 2.0 Graph Workflows (`google.golang.org/adk/v2`)**, **Google Cloud Agent Platform / Agent Runtime (`agents-cli` `adk_go`)**, **Gemini (`google.golang.org/genai`)**, and **LINE Messaging API SDK v8 (`github.com/line/line-bot-sdk-go/v8`)**.

![LINE Card Assistant + Google Cloud Agent Platform Architecture](docs/architecture.png)

---

## ✨ Key Features

- **Dual-Channel Agent Platform & LINE Webhook Architecture (`adk_go` + `agent_runtime`)**:
  - **Root Agent Factory (`app.NewRootAgent`)**: Exposes the entire ADK 2.0 Graph Workflow as a standard `agent.Agent` (`line_card_assistant`) compatible with **Vertex AI Agent Runtime (`agentengine`)**, **ADK WebUI Playground (`webui`)**, and **Agent-to-Agent Protocol (`a2a` / `/.well-known/agent-card.json`)**, while preserving the standalone **LINE Webhook server (`cmd/server`)**.
  - **Multi-Backend Managed Sessions (`sessions.NewService`)**: Seamlessly switches between in-memory sessions (`SESSION_TYPE=in_memory`), **Vertex AI Agent Engine Managed Sessions (`SESSION_TYPE=agent_engine` / `session/vertexai`)**, and **Cloud SQL PostgreSQL (`SESSION_TYPE=cloud_sql`)**, enabling **stateless cross-instance HITL Pause & Resume**.
  - **GCS Artifact Storage (`sessions.NewArtifactService`)**: Offloads binary business card photos from `Session.State` into versioned **Google Cloud Storage Artifacts (`artifact/gcsartifact`)** (`ARTIFACT_BUCKET_NAME` / `LOGS_BUCKET_NAME`), storing only lightweight artifact references (`card_image_artifact`, `card_image_version`) in session state.
  - **Cloud Trace & OpenTelemetry Observability (`observability.go`)**: Automatically exports spans and structured logs to `telemetry.googleapis.com` via OTLP with Google ADC authentication.
- **Multimodal Vision OCR (`VisionOCRNode`)**: Upload a business card photo in LINE to automatically extract structured fields (`Name`, `Title`, `Company`, `Email`, `Phone`, `Address`, `Website`, `TaxID`).
- **ADK 2.0 Parallel Fan-Out / Fan-In Enrichment (`AddFanOut` / `AddFanIn`)**:
  - **`CleanAndDedupNode` [DB 查重與驗證]**: Normalizes Taiwan phone numbers (`09xx-xxx-xxx` $\rightarrow$ `+886-9xx-xxx-xxx`) and checks `CardStore` for existing contacts or job title promotions.
  - **`CompanyEnrichNode` [公司背景搜尋]**: Runs an ADK `llmagent` equipped with `geminitool.GoogleSearch{}` to summarize the company's core business and generate 3–5 industry tags.
  - **`ContextMatchNode` [過往互動檢索]**: Infers the meeting event/context (`MetContext`, e.g., `2026 Cloud Summit Taipei`) from recent user messages.
- **Human-in-the-Loop (HITL) Confirmation & Cyclic Edit Loop**:
  - Pauses workflow execution using `workflow.NewRequestInputEvent` and sends an interactive **LINE Flex Message Bubble** preview with two action buttons:
    - **「✅ 確認存檔」** (`action=confirm_save` / `action=confirm`) $\rightarrow$ Routes to `SaveCardNode` **[寫入 DB + 產出破冰信草稿]**.
    - **「✏️ 修改欄位 / 補備註」** (`action=edit` with `openKeyboard` or direct text reply like `「他名字是王太明，在 DevFest 認識的」`) $\rightarrow$ Routes to `ApplyEditNode` **[Re-entry 迴圈修正節點]** $\rightarrow$ loops back to `BuildPreviewNode` $\rightarrow$ `HITLConfirmNode`.
- **Natural Language Hybrid Search & Flex Carousel**:
  - **Query Rewrite (`QueryIntentNode`)**: Converts conversational queries (e.g., `「幫我找上週在 Cloud Summit 認識做 AI 的人」`) into structured `SearchFilter` criteria.
  - **SQL + Vector / Keyword Hybrid Search (`HybridSearchNode` + `BroadSearchNode`)**: Runs strict structured + semantic keyword matching with automatic fallback to fuzzy token scoring.
  - **Flex Carousel (`FormatCarouselNode`)**: Renders up to 5 cards in a **LINE Flex Message Carousel** with one-tap `tel:`, `mailto:`, and **「✉️ 撰寫破冰跟進信」** (`action=draft_email&card_id=<id>`) actions.
- **Cloud Run Self-Identity (ADC) & Agent Platform API Key Support**:
  - Supports **Cloud Run Service Account Self-Identity (Application Default Credentials)** on Vertex AI (`GOOGLE_GENAI_USE_VERTEXAI=true`), **Vertex AI / Agent Platform API Key**, **Gemini API Key**, and zero-config **Dry-Run MockLLM Mode** for local testing.

---

## 🏗️ Workflow Architecture

### 1. System Flowchart

```text
[LINE Webhook (/webhook)]       [Agent Platform / ADK WebUI / A2A (/api, /a2a)]
            │                                       │
            └───────────────────┬───────────────────┘
                                ▼
                     [app.NewRootAgent(ctx)]
                                │
                                ▼
                    [Router Node (AddRoutes)]
  ├── (收到名片照片) ──► [Vision OCR 節點] 
  │                           │
  │                    (AddFanOut 平行處理)
  │            ┌──────────────┼──────────────┐
  │            ▼              ▼              ▼
  │     [DB 查重與驗證]  [公司背景搜尋]  [過往互動檢索]
  │            └──────────────┼──────────────┘
  │                    (AddFanIn 匯聚)
  │                           ▼
  │              [生成 Flex Message 預覽卡]
  │                           │
  │            ⏸️ NewRequestInputEvent (HITL 暫停等待)
  │                           │
  │      ┌────────────────────┴────────────────────┐
  │      ▼ (點擊：確認存檔)                         ▼ (點擊：修改欄位 / 補備註)
  │ [寫入 DB + 產出破冰信草稿]              [Re-entry 迴圈修正節點]
  │
  └── (收到查詢文字) ──► [Query Rewrite] ──► [SQL + Vector 混合檢索] ──► [Flex Carousel 名片輪播]
```

### 2. Mermaid Graph Diagram

```mermaid
flowchart TD
    Webhook["LINE Webhook (/webhook)"] --> RootAgent["app.NewRootAgent (line_card_assistant)"]
    AgentPlatform["Agent Platform / ADK WebUI / A2A"] --> RootAgent
    RootAgent --> Router["RouterNode (AddRoute)"]

    %% Path A: Card Ingestion & HITL
    Router -->|"收到名片照片 (route: ingest)"| VisionOCR["VisionOCRNode (Vision OCR 節點)"]
    VisionOCR -->|"AddFanOut"| CleanDedup["CleanAndDedupNode (DB 查重與驗證)"]
    VisionOCR -->|"AddFanOut"| CompanyEnrich["CompanyEnrichNode (公司背景搜尋 + GoogleSearch)"]
    VisionOCR -->|"AddFanOut"| ContextMatch["ContextMatchNode (過往互動檢索)"]

    CleanDedup -->|"AddFanIn"| JoinPreview["JoinPreviewNode"]
    CompanyEnrich -->|"AddFanIn"| JoinPreview
    ContextMatch -->|"AddFanIn"| JoinPreview
    JoinPreview --> BuildPreview["BuildPreviewNode (生成 Flex Message 預覽卡)"]

    BuildPreview --> HITL["HITLConfirmNode (⏸️ NewRequestInputEvent 暫停等待)"]
    HITL -->|"點擊：確認存檔 (route: confirm)"| SaveCard["SaveCardNode (寫入 DB + 產出破冰信草稿)"]
    HITL -->|"點擊：修改欄位 / 補備註 (route: edit)"| ApplyEdit["ApplyEditNode (Re-entry 迴圈修正節點)"]
    ApplyEdit -->|"route: preview"| BuildPreview

    %% Path B: Hybrid Search
    Router -->|"收到查詢文字 (route: search)"| QueryIntent["QueryIntentNode (Query Rewrite)"]
    QueryIntent --> HybridSearch["HybridSearchNode (SQL + Vector 混合檢索)"]
    HybridSearch -->|"route: found"| FormatCarousel["FormatCarouselNode (Flex Carousel 名片輪播)"]
    HybridSearch -->|"route: empty (0 筆自動降級)"| BroadSearch["BroadSearchNode (模糊檢索 Fallback)"]
    BroadSearch --> FormatCarousel

    %% Path C: Standalone Email Draft Action
    Router -->|"Postback: draft_email (route: email)"| EmailDraft["EmailDraftNode (撰寫破冰跟進信)"]
```

### 3. Flowchart-to-Code Mapping

| 流程圖節點 | 對應程式碼節點 / 函式 | 檔案位置 |
| :--- | :--- | :--- |
| `[app.NewRootAgent(ctx)]` | `app.NewRootAgent` / `app.NewRootAgentWithDeps` | `app/agent.go` |
| `[Agent Platform / ADK WebUI / A2A]` | `universal.NewLauncher` (`webui`, `a2a`, `agentengine`, `appinfo`) | `main.go`, `appinfo/appinfo.go` |
| `[LINE Webhook]` | `Handler.ServeHTTP` | `internal/linebot/handler.go` |
| `[Router Node (AddRoutes)]` | `RouterNode` + `eb.AddRoute(...)` | `internal/workflow/graph.go`, `internal/workflow/nodes_ingest.go` |
| `[Vision OCR 節點]` | `VisionOCRNode` (`newVisionOCRNode`) | `internal/workflow/nodes_ingest.go` |
| `(AddFanOut 平行處理)` | `eb.AddFanOut(visionOCRNode, ...)` | `internal/workflow/graph.go` |
| `[DB 查重與驗證]` | `CleanAndDedupNode` (`newCleanAndDedupNode`) | `internal/workflow/nodes_ingest.go` |
| `[公司背景搜尋]` | `CompanyEnrichNode` (`llmagent` + `geminitool.GoogleSearch{}`) | `internal/workflow/nodes_ingest.go` |
| `[過往互動檢索]` | `ContextMatchNode` (`newContextMatchNode`) | `internal/workflow/nodes_ingest.go` |
| `(AddFanIn 匯聚)` + `[生成 Flex Message 預覽卡]` | `eb.AddFanIn(...)` $\rightarrow$ `BuildPreviewNode` | `internal/workflow/graph.go`, `internal/workflow/nodes_ingest.go` |
| `⏸️ NewRequestInputEvent (HITL 暫停等待)` | `HITLConfirmNode` (`workflow.NewRequestInputEvent`) | `internal/workflow/nodes_ingest.go` |
| `[寫入 DB + 產出破冰信草稿]` | `SaveCardNode` (`cardStore.Save` + `generateDraftEmail`) | `internal/workflow/nodes_ingest.go` |
| `[Re-entry 迴圈修正節點]` | `ApplyEditNode` $\rightarrow$ `BuildPreviewNode` | `internal/workflow/nodes_ingest.go` |
| `[Query Rewrite]` | `QueryIntentNode` (`newQueryIntentNode`) | `internal/workflow/nodes_search.go` |
| `[SQL + Vector 混合檢索]` | `HybridSearchNode` + `BroadSearchNode` | `internal/workflow/nodes_search.go`, `internal/store/memory.go` |
| `[Flex Carousel 名片輪播]` | `FormatCarouselNode` (`BuildSearchCarouselFlexJSON`) | `internal/workflow/nodes_search.go`, `internal/linebot/flex.go` |

---

## 📂 Project Structure

```text
.
├── main.go                         # ADK Universal Launcher entrypoint (WebUI, A2A, Agent Engine, AppInfo)
├── apploader.go                    # Single-agent loader for ADK launcher
├── appurl.go                       # Resolves public service URL for A2A & WebUI
├── observability.go                # OpenTelemetry OTLP exporter to Google Cloud Trace & Logging
├── agents-cli-manifest.yaml        # Google Cloud agents-cli (adk_go + agent_runtime) manifest
├── app/
│   ├── agent.go                    # Root ADK 2.0 Graph Workflow agent factory (app.NewRootAgent)
│   └── agent_test.go               # Root agent & workflow integration tests
├── appinfo/
│   ├── appinfo.go                  # /app-info sublauncher exposing agent tree metadata for Evals
│   └── appinfo_test.go
├── sessions/
│   ├── sessions.go                 # Multi-backend session factory (in_memory, agent_engine, cloud_sql)
│   ├── artifacts.go                # GCS / In-Memory Artifact service factory (artifact/gcsartifact)
│   └── sessions_test.go            # Session & Artifact service unit tests
├── cmd/
│   └── server/
│       └── main.go                 # Standalone LINE Webhook HTTP server (/webhook, /healthz)
├── internal/
│   ├── config/
│   │   └── config.go               # Env var loader (Cloud Run Self-Identity ADC, Vertex AI & API keys)
│   ├── models/
│   │   └── card.go                 # BusinessCard, SearchFilter, CardWorkflowState
│   ├── store/
│   │   ├── store.go                # CardStore / CardRepository interface
│   │   ├── memory.go               # Thread-safe in-memory store with 4 seeded Taiwan cards
│   │   └── sqlite.go               # SQLite persistent store implementation
│   ├── workflow/
│   │   ├── graph.go                # ADK 2.0 Graph Workflow builder, runner & MockLLM
│   │   ├── nodes_ingest.go         # Ingestion, Fan-Out/Fan-In, HITL & Edit Loop nodes
│   │   ├── nodes_search.go         # Search intent, Hybrid/Broad search, Carousel & Email nodes
│   │   └── graph_test.go           # End-to-end ADK 2.0 workflow & HITL unit tests
│   └── linebot/
│       ├── handler.go              # LINE Messaging API v8 webhook handler
│       ├── handler_test.go         # Webhook integration tests
│       └── flex.go                 # Flex Message Bubble & Carousel JSON builders
├── docs/
│   └── architecture.png            # System architecture diagram
├── Dockerfile                      # Multi-stage distroless container image for Agent Runtime & Cloud Run
├── Makefile                        # playground, local-backend, line-server, deploy, test targets
└── README.md
```

---

## ⚙️ Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `PORT` | HTTP server port | `8080` |
| `LINE_CHANNEL_SECRET` | LINE Messaging API Channel Secret | *(Dry-Run if empty)* |
| `LINE_CHANNEL_ACCESS_TOKEN` | LINE Messaging API Channel Access Token | *(Dry-Run if empty)* |
| `GEMINI_MODEL` | Gemini model name | `gemini-3.8-flash` |
| `GOOGLE_GENAI_USE_VERTEXAI` | Set to `true` (or `1`) to use Vertex AI / Agent Platform backend | `false` (auto `true` if `GOOGLE_CLOUD_PROJECT` is set) |
| `GOOGLE_CLOUD_PROJECT` | GCP Project ID for Cloud Run Self-Identity (ADC) | *(Auto-detected on Cloud Run via Metadata Server)* |
| `GOOGLE_CLOUD_LOCATION` | Vertex AI location/region | `global` |
| `AGENT_PLATFORM_API_KEY` | Optional Agent Platform / Vertex AI Express API Key | *(Uses Self-Identity ADC if omitted on Vertex AI)* |
| `GOOGLE_API_KEY` | Optional Gemini Developer API Key | *(MockLLM if neither ADC nor API Key is set)* |
| `SESSION_TYPE` | Session backend (`in_memory`, `agent_engine` / `agent_platform_sessions`, `cloud_sql`) | `in_memory` (auto `agent_engine` if `GOOGLE_CLOUD_AGENT_ENGINE_ID` is set) |
| `ARTIFACT_BUCKET_NAME` | GCS bucket name for binary card photo artifacts (`artifact/gcsartifact`) | *(Falls back to `LOGS_BUCKET_NAME`, then `artifact.InMemoryService()`)* |

---

## ☁️ Deploying to Google Cloud Run (Self-Identity / Vertex AI Agent Platform)

### Option A: Cloud Run Self-Identity (Recommended — Zero API Key via Service Account ADC)

When deployed on Cloud Run with `GOOGLE_GENAI_USE_VERTEXAI=true`, the service automatically uses its attached **Cloud Run Service Account (Application Default Credentials)** and auto-detects `GOOGLE_CLOUD_PROJECT` from the GCP Metadata Server:

```bash
# 1. 建立或指定 Cloud Run 服務帳號並授予 Vertex AI User 權限 (Self-Identity)
PROJECT_ID=$(gcloud config get-value project)
SA_NAME="line-card-assistant-sa"
SA_EMAIL="${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"

gcloud iam service-accounts create "${SA_NAME}" \
  --display-name="LINE Card Assistant Cloud Run SA" || true

gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/aiplatform.user"

# 2. 部署至 Cloud Run，綁定該 Service Account 並啟用 Vertex AI Backend
gcloud run deploy line-card-assistant \
  --source . \
  --region asia-east1 \
  --service-account "${SA_EMAIL}" \
  --allow-unauthenticated \
  --set-env-vars="GOOGLE_GENAI_USE_VERTEXAI=true,GOOGLE_CLOUD_PROJECT=${PROJECT_ID},GOOGLE_CLOUD_LOCATION=global,LINE_CHANNEL_SECRET=${LINE_CHANNEL_SECRET},LINE_CHANNEL_ACCESS_TOKEN=${LINE_CHANNEL_ACCESS_TOKEN}"
```

### Option B: Cloud Run with Agent Platform API Key (Vertex AI Express Mode)

If you are using an **Agent Platform / Vertex AI API Key** on Cloud Run:

```bash
gcloud run deploy line-card-assistant \
  --source . \
  --region asia-east1 \
  --allow-unauthenticated \
  --set-env-vars="GOOGLE_GENAI_USE_VERTEXAI=true,AGENT_PLATFORM_API_KEY=${AGENT_PLATFORM_API_KEY},LINE_CHANNEL_SECRET=${LINE_CHANNEL_SECRET},LINE_CHANNEL_ACCESS_TOKEN=${LINE_CHANNEL_ACCESS_TOKEN}"
```

---

## 🚀 Local Quickstart

### 1. Run Unit & Workflow Tests (Zero External Keys Required)

```bash
make test
# or directly:
go test -race -v ./...
```

### 2. Launch Interactive ADK WebUI Playground (`:8501`)

```bash
make playground
# Open http://127.0.0.1:8501/ui/
```

### 3. Launch Agent Runtime / A2A Local Backend (`:8000`)

```bash
make local-backend
# Agent Card: curl http://127.0.0.1:8000/.well-known/agent-card.json
# App Info:   curl http://127.0.0.1:8000/app-info
```

### 4. Launch Standalone LINE Webhook Server (`:8080`)

```bash
make line-server
# Health Check: curl http://localhost:8080/healthz
```
