package models

import "time"

// BusinessCard represents a structured business card record with AI enrichment.
type BusinessCard struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"` // LINE User ID of the owner
	Name           string    `json:"name"`
	Title          string    `json:"title"`
	Company        string    `json:"company"`
	Email          string    `json:"email"`
	Phone          string    `json:"phone"` // Normalized format: +886-9xx-xxx-xxx
	Address        string    `json:"address"`
	TaxID          string    `json:"tax_id"`
	Website        string    `json:"website"`
	RawOCR         string    `json:"raw_ocr"`
	CompanySummary string    `json:"company_summary"` // Enriched by Google Search Grounding
	CompanyInfo    string    `json:"company_info"`    // Enriched via Google Search Grounding
	Tags           []string  `json:"tags"`            // Auto-generated industry/role tags
	MetContext     string    `json:"met_context"`     // e.g., "2026 Google Cloud Summit Taipei"
	RawImageURL    string    `json:"raw_image_url"`
	MetDate        time.Time `json:"met_date"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CardWorkflowState binds to ADK 2.0 Session State using state tags.
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

// Clone returns a deep copy of BusinessCard.
func (c *BusinessCard) Clone() *BusinessCard {
	if c == nil {
		return nil
	}
	cp := *c
	if c.Tags != nil {
		cp.Tags = make([]string, len(c.Tags))
		copy(cp.Tags, c.Tags)
	}
	return &cp
}

// SearchFilter represents structured and semantic search parameters extracted from user queries.
type SearchFilter struct {
	UserID        string     `json:"user_id"`
	Keywords      []string   `json:"keywords"` // Semantic/fuzzy match against Name, Company, Title, Tags, MetContext
	Company       string     `json:"company"`
	Title         string     `json:"title"`
	RoleFilter    string     `json:"role_filter,omitempty"`
	ContextFilter string     `json:"context_filter,omitempty"`
	Tag           string     `json:"tag"`
	DateRange     *DateRange `json:"date_range,omitempty"`
	TargetCardID  string     `json:"target_card_id,omitempty"` // Used when action=draft_email
}

// DateRange specifies an inclusive start and end time filter.
type DateRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
