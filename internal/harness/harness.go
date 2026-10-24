package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// EnvHarnessDir is the environment variable pointing to the active c1..c8 Harness directory.
const EnvHarnessDir = "LINE_CARD_HARNESS_DIR"

// AlwaysOnMaxBytes matches google-research/harness2 OPENCODE_SURFACE.always_on_max (24,000 bytes).
const AlwaysOnMaxBytes = 24000

// AlwaysOnFiles lists the four c1..c4 always-on files mounted into the agent context.
var AlwaysOnFiles = []string{
	"systemprompt.md",
	"guardrails.md",
	"LongTermMEMORY.md",
	"tool_descriptions/tool_guidance.md",
}

// SkillSpec represents a c6 on-demand skill (skills/<name>/SKILL.md).
type SkillSpec struct {
	Name        string
	Description string
	Body        string
	RelPath     string
}

// SubAgentSpec represents a c5 delegated sub-agent (sub_agents/<name>/AGENT.md).
type SubAgentSpec struct {
	Name        string
	Mode        string
	Description string
	Body        string
	RelPath     string
}

// Harness holds the loaded c1..c8 editable component tree for LINE Card Assistant.
type Harness struct {
	RootDir        string
	SystemPrompt   string
	Guardrails     string
	LongTermMemory string
	ToolGuidance   string
	Skills         map[string]SkillSpec
	SubAgents      map[string]SubAgentSpec
	Scripts        map[string]string
	Plugins        map[string]string
	Files          map[string]string
}

var (
	reFrontmatter = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n(.*)$`)
	reSkillName   = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]*$`)
)

// LoadActive loads the Harness from LINE_CARD_HARNESS_DIR if set,
// otherwise tries "./harness/base", or falls back to the built-in default tree.
func LoadActive() *Harness {
	if envDir := strings.TrimSpace(os.Getenv(EnvHarnessDir)); envDir != "" {
		if h, err := LoadFromDir(envDir); err == nil {
			return h
		}
	}
	for _, candidate := range []string{"harness/base", "../harness/base", "../../harness/base"} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if h, err := LoadFromDir(candidate); err == nil {
				return h
			}
		}
	}
	return DefaultBaseHarness()
}

// DefaultBaseHarness returns the unmodified baseline c1..c8 component tree.
func DefaultBaseHarness() *Harness {
	files := map[string]string{
		"systemprompt.md": `# LINE Card Assistant — Core System Prompt (c1)
你是一位專業的繁體中文 AI 商務名片助理（LINE AI 名片王），運行於 Google ADK 2.0 Go Graph Workflow。
你的核心職責包含：
1. [VisionOCRNode]: 從名片影像中精準擷取姓名 (name)、職稱 (title)、公司 (company)、信箱 (email)、電話 (phone)、地址 (address)、網站 (website) 與原始文字 (raw_ocr)。
2. [CompanyEnrichNode]: 透過 Google Search 檢索公司背景，產出 2 句話繁體中文簡介 (company_info) 與 3~5 個產業標籤 (tags)。
3. [ContextMatchNode]: 從使用者近期對話推論相遇場合 (met_context)。
4. [QueryIntentNode]: 將自然語言搜尋語句解析為結構化 SearchFilter (keywords, company, title, tag)。
5. [EmailDraftNode]: 結合相遇場合與公司背景，撰寫得體的繁體中文商務破冰跟進信 (Follow-up Email)。
`,
		"guardrails.md": `# Guardrails & Format Invariants (c2)
- 所有 JSON 輸出節點（VisionOCRNode、CompanyEnrichNode、QueryIntentNode）必須僅輸出合法 JSON，不得包裹未經請求的額外說明。
- 電話號碼須正規化為台灣國際格式（如 +886-912-345-678 或 +886-2-xxxx-xxxx）。
- 嚴禁捏造名片上未出現的電子郵件或電話號碼。
`,
		"LongTermMEMORY.md": `# Long-Term Domain Memory (c3)
<!-- Populated across iterations by the Harness² Experience Reflector (ReflectorPrompt) -->
`,
		"tool_descriptions/tool_guidance.md": `# Tool Interface Guidance (c4)
- google_search: 查詢公司官方網站、核心產品線與產業定位。
- format_company_profile: 將搜尋所得之公司簡介 (summary) 與 3~5 個產業標籤 (tags) 轉為標準結構化 JSON。
`,
	}
	h, _ := FromTreeMap("builtin://base", files)
	return h
}

// LoadFromDir reads all c1..c8 files from a directory on disk into a Harness struct.
func LoadFromDir(dir string) (*Harness, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve harness dir %q: %w", dir, err)
	}
	info, err := os.Stat(absDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("harness dir %q does not exist or is not a directory", absDir)
	}

	files := make(map[string]string)
	err = filepath.WalkDir(absDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, "README.md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = string(raw)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk harness dir %q: %w", absDir, err)
	}
	return FromTreeMap(absDir, files)
}

// FromTreeMap constructs and parses a Harness from an in-memory map of relative path -> content.
func FromTreeMap(rootDir string, files map[string]string) (*Harness, error) {
	h := &Harness{
		RootDir:   rootDir,
		Skills:    make(map[string]SkillSpec),
		SubAgents: make(map[string]SubAgentSpec),
		Scripts:   make(map[string]string),
		Plugins:   make(map[string]string),
		Files:     make(map[string]string, len(files)),
	}
	for k, v := range files {
		norm := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(k)), "./")
		h.Files[norm] = v
	}

	h.SystemPrompt = h.Files["systemprompt.md"]
	h.Guardrails = h.Files["guardrails.md"]
	h.LongTermMemory = h.Files["LongTermMEMORY.md"]
	h.ToolGuidance = h.Files["tool_descriptions/tool_guidance.md"]

	for rel, content := range h.Files {
		switch {
		case strings.HasPrefix(rel, "skills/") && strings.HasSuffix(rel, "/SKILL.md"):
			parts := strings.Split(rel, "/")
			if len(parts) == 3 {
				dirName := parts[1]
				meta, body := parseYAMLFrontmatter(content)
				skillName := meta["name"]
				if skillName == "" {
					skillName = dirName
				}
				h.Skills[dirName] = SkillSpec{
					Name:        skillName,
					Description: meta["description"],
					Body:        strings.TrimSpace(body),
					RelPath:     rel,
				}
			}
		case strings.HasPrefix(rel, "sub_agents/") && strings.HasSuffix(rel, "/AGENT.md"):
			parts := strings.Split(rel, "/")
			if len(parts) == 3 {
				dirName := parts[1]
				meta, body := parseYAMLFrontmatter(content)
				h.SubAgents[dirName] = SubAgentSpec{
					Name:        dirName,
					Mode:        meta["mode"],
					Description: meta["description"],
					Body:        strings.TrimSpace(body),
					RelPath:     rel,
				}
			}
		case strings.HasPrefix(rel, "scripts/") && strings.HasSuffix(rel, ".py"):
			h.Scripts[rel] = content
		case strings.HasPrefix(rel, "plugin/") && strings.HasSuffix(rel, ".ts"):
			h.Plugins[rel] = content
		}
	}

	return h, nil
}

// AlwaysOnBytes returns the total byte size of the c1..c4 always-on files.
func (h *Harness) AlwaysOnBytes() int {
	if h == nil {
		return 0
	}
	return len(h.SystemPrompt) + len(h.Guardrails) + len(h.LongTermMemory) + len(h.ToolGuidance)
}

// Validate enforces Google Research Harness² OPENCODE_SURFACE invariants on the c1..c8 tree.
func (h *Harness) Validate() error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	var problems []string

	for _, reqFile := range AlwaysOnFiles {
		if strings.TrimSpace(h.Files[reqFile]) == "" {
			problems = append(problems, fmt.Sprintf("missing or empty always-on component %q", reqFile))
		}
	}
	if total := h.AlwaysOnBytes(); total > AlwaysOnMaxBytes {
		problems = append(problems, fmt.Sprintf("always-on c1..c4 byte count %d exceeds cap %d (every token here competes with the agent's output budget)", total, AlwaysOnMaxBytes))
	}

	combinedAlwaysOn := h.SystemPrompt + "\n" + h.Guardrails
	for dirName, skill := range h.Skills {
		if !reSkillName.MatchString(dirName) {
			problems = append(problems, fmt.Sprintf("invalid skill directory name %q", dirName))
		}
		if skill.Name != dirName {
			problems = append(problems, fmt.Sprintf("%s frontmatter name %q must match directory name %q", skill.RelPath, skill.Name, dirName))
		}
		if strings.TrimSpace(skill.Description) == "" {
			problems = append(problems, fmt.Sprintf("%s must include a non-empty description in YAML frontmatter", skill.RelPath))
		}
		if !strings.Contains(combinedAlwaysOn, dirName) {
			problems = append(problems, fmt.Sprintf("skill %q is never referenced in systemprompt.md or guardrails.md; add a trigger line naming `%s`", dirName, dirName))
		}
	}

	for dirName, sa := range h.SubAgents {
		if sa.Mode != "subagent" {
			problems = append(problems, fmt.Sprintf("%s must declare frontmatter `mode: subagent` (got %q)", sa.RelPath, sa.Mode))
		}
		if strings.TrimSpace(sa.Description) == "" {
			problems = append(problems, fmt.Sprintf("%s must declare a non-empty `description:` in frontmatter", sa.RelPath))
		}
		_ = dirName
	}

	if len(problems) > 0 {
		return fmt.Errorf("harness validation failed:\n- %s", strings.Join(problems, "\n- "))
	}
	return nil
}

// MatchSkills evaluates which c6 skills should be dynamically loaded for a given node and task input.
func (h *Harness) MatchSkills(nodeName string, taskInput string) []SkillSpec {
	if h == nil || len(h.Skills) == 0 {
		return nil
	}
	names := make([]string, 0, len(h.Skills))
	for name := range h.Skills {
		names = append(names, name)
	}
	sort.Strings(names)

	var matched []SkillSpec
	for _, name := range names {
		skill := h.Skills[name]
		switch name {
		case "jp_card_ocr", "multilingual_ocr":
			if nodeName == "VisionOCRNode" || nodeName == "CompanyEnrichNode" || nodeName == "CleanAndDedupNode" {
				matched = append(matched, skill)
			}
		case "grounding_verification":
			if nodeName == "CompanyEnrichNode" {
				matched = append(matched, skill)
			}
		case "tw_semicon_search", "colloquial_query_expansion":
			if nodeName == "QueryIntentNode" || nodeName == "BroadSearchNode" {
				matched = append(matched, skill)
			}
		case "hitl_multi_edit":
			if nodeName == "ApplyEditNode" || nodeName == "CleanAndDedupNode" {
				matched = append(matched, skill)
			}
		case "executive_email_drafter", "executive_icebreaker":
			if nodeName == "EmailDraftNode" || nodeName == "SaveCardNode" {
				matched = append(matched, skill)
			}
		default:
			if strings.Contains(strings.ToLower(taskInput), strings.ToLower(name)) {
				matched = append(matched, skill)
			}
		}
	}
	return matched
}

// ComposeNodePrompt enriches a node's base prompt with the active c1..c4 always-on components
// and any dynamically triggered c6 skills, returning both the composed prompt and the list of triggered skill names.
func (h *Harness) ComposeNodePrompt(nodeName string, basePrompt string, taskInput string) (string, []string) {
	if h == nil {
		return basePrompt, nil
	}
	skills := h.MatchSkills(nodeName, taskInput)
	skillNames := make([]string, 0, len(skills))

	var sb strings.Builder
	sb.WriteString("--- [HARNESS c1..c4 CONTEXT] ---\n")
	if strings.TrimSpace(h.SystemPrompt) != "" {
		sb.WriteString("[System Prompt - systemprompt.md]\n")
		sb.WriteString(strings.TrimSpace(h.SystemPrompt))
		sb.WriteString("\n")
	}
	if strings.TrimSpace(h.Guardrails) != "" {
		sb.WriteString("[Guardrails - guardrails.md]\n")
		sb.WriteString(strings.TrimSpace(h.Guardrails))
		sb.WriteString("\n")
	}
	if strings.TrimSpace(h.LongTermMemory) != "" {
		sb.WriteString("[Long-Term Memory - LongTermMEMORY.md]\n")
		sb.WriteString(strings.TrimSpace(h.LongTermMemory))
		sb.WriteString("\n")
	}
	if strings.TrimSpace(h.ToolGuidance) != "" {
		sb.WriteString("[Tool & Node Guidance - tool_descriptions/tool_guidance.md]\n")
		sb.WriteString(strings.TrimSpace(h.ToolGuidance))
		sb.WriteString("\n")
	}
	for _, sk := range skills {
		skillNames = append(skillNames, sk.Name)
		sb.WriteString(fmt.Sprintf("\n[Active Skill - %s]\n%s\n", sk.Name, sk.Body))
	}
	sb.WriteString(fmt.Sprintf("\n[Current Node: %s]\n%s", nodeName, strings.TrimSpace(basePrompt)))
	return sb.String(), skillNames
}

func parseYAMLFrontmatter(content string) (map[string]string, string) {
	meta := make(map[string]string)
	m := reFrontmatter.FindStringSubmatch(strings.TrimSpace(content))
	if len(m) != 3 {
		return meta, content
	}
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, ':'); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`)
			meta[k] = v
		}
	}
	return meta, m[2]
}
