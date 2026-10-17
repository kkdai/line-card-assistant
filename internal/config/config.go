package config

import (
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const DefaultModelName = "gemini-2.5-flash"

// Config holds the runtime configuration loaded from environment variables.
type Config struct {
	Port                   string
	LineChannelSecret      string
	LineChannelAccessToken string
	GoogleAPIKey           string
	UseVertexAI            bool
	GoogleCloudProject     string
	GoogleCloudLocation    string
	ModelName              string
}

// Load reads environment variables and returns a populated Config.
func Load() *Config {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	modelName := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if modelName == "" {
		modelName = DefaultModelName
	}

	apiKey := firstNonEmpty(
		os.Getenv("GOOGLE_API_KEY"),
		os.Getenv("GEMINI_API_KEY"),
		os.Getenv("AGENT_PLATFORM_API_KEY"),
	)

	project := firstNonEmpty(
		os.Getenv("GOOGLE_CLOUD_PROJECT"),
		os.Getenv("GCP_PROJECT"),
	)
	location := firstNonEmpty(
		os.Getenv("GOOGLE_CLOUD_LOCATION"),
		os.Getenv("GOOGLE_CLOUD_REGION"),
	)
	if location == "" {
		location = "us-central1"
	}

	useVertexEnv := strings.ToLower(firstNonEmpty(
		os.Getenv("GOOGLE_GENAI_USE_VERTEXAI"),
		os.Getenv("GOOGLE_GENAI_USE_ENTERPRISE"),
	))
	useVertexAI := useVertexEnv == "true" || useVertexEnv == "1" || project != ""

	// When running on Cloud Run (K_SERVICE is set) with Vertex AI / Self-Identity enabled,
	// auto-detect the GCP Project ID from the Cloud Run Metadata Server if not explicitly set.
	if (useVertexAI || os.Getenv("K_SERVICE") != "") && project == "" && apiKey == "" {
		if metaProject := detectCloudRunProjectID(); metaProject != "" {
			project = metaProject
			useVertexAI = true
		}
	}

	return &Config{
		Port: port,
		LineChannelSecret: firstNonEmpty(
			os.Getenv("LINE_CHANNEL_SECRET"),
			os.Getenv("ChannelSecret"),
		),
		LineChannelAccessToken: firstNonEmpty(
			os.Getenv("LINE_CHANNEL_ACCESS_TOKEN"),
			os.Getenv("ChannelAccessToken"),
		),
		GoogleAPIKey:        apiKey,
		UseVertexAI:         useVertexAI,
		GoogleCloudProject:  project,
		GoogleCloudLocation: location,
		ModelName:           modelName,
	}
}

// IsDryRunLLM returns true when neither an API key nor Vertex AI Self-Identity (Project/ADC) is configured.
func (c *Config) IsDryRunLLM() bool {
	if c == nil {
		return true
	}
	if c.GoogleAPIKey != "" {
		return false
	}
	if c.UseVertexAI && c.GoogleCloudProject != "" {
		return false
	}
	return true
}

// IsDryRunLINE returns true when LINE credentials are missing, enabling console log fallback.
func (c *Config) IsDryRunLINE() bool {
	return c == nil || c.LineChannelAccessToken == "" || c.LineChannelSecret == ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func detectCloudRunProjectID() string {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	req, err := http.NewRequest(http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/project/project-id", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}
