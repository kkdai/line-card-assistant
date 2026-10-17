package store

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kkdai/line-card-assistant/internal/models"
)

// DefaultDemoUserID is the default user ID used for pre-seeded business cards and Dry-Run testing.
const DefaultDemoUserID = "demo"

// MemoryStore is a thread-safe in-memory implementation of CardStore.
type MemoryStore struct {
	mu    sync.RWMutex
	cards map[string]*models.BusinessCard
}

// NewMemoryStore creates a new MemoryStore pre-seeded with realistic mock business cards.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{
		cards: make(map[string]*models.BusinessCard),
	}
	s.seedDefaultCards()
	return s
}

// NewEmptyMemoryStore creates an empty MemoryStore without seeded data.
func NewEmptyMemoryStore() *MemoryStore {
	return &MemoryStore{
		cards: make(map[string]*models.BusinessCard),
	}
}

func (s *MemoryStore) seedDefaultCards() {
	now := time.Now()
	lastWeek := now.AddDate(0, 0, -5)
	twoWeeksAgo := now.AddDate(0, 0, -12)
	lastMonth := now.AddDate(0, 0, -25)

	seeded := []*models.BusinessCard{
		{
			ID:          "card-seed-1",
			UserID:      "demo",
			Name:        "林雅婷 (Yating Lin)",
			Title:       "Senior AI Solutions Architect",
			Company:     "Google Cloud Taiwan",
			Email:       "yating.lin@example.com",
			Phone:       "+886-912-345-678",
			Address:     "台北市信義區信義路五段7號 (台北101)",
			Website:     "https://cloud.google.com",
			RawOCR:      "Google Cloud Taiwan | 林雅婷 Yating Lin | Senior AI Solutions Architect | yating.lin@example.com | 0912-345-678",
			CompanyInfo: "Google Cloud 台灣提供企業雲端運算、Gemini AI 與 Vertex AI 解決方案，協助企業進行數位與 AI 轉型。",
			Tags:        []string{"AI", "Cloud", "SaaS", "Vertex AI", "Tech"},
			MetContext:  "2026 Cloud Summit Taipei",
			MetDate:     lastWeek,
			CreatedAt:   lastWeek,
			UpdatedAt:   lastWeek,
		},
		{
			ID:          "card-seed-2",
			UserID:      "demo",
			Name:        "陳建宏 (David Chen)",
			Title:       "VP of Engineering",
			Company:     "Appier (沛星互動科技)",
			Email:       "david.chen@appier.example.com",
			Phone:       "+886-922-888-168",
			Address:     "台北市信義區松仁路100號",
			Website:     "https://www.appier.com",
			RawOCR:      "Appier | 陳建宏 David Chen | VP of Engineering | david.chen@appier.example.com | +886 922 888 168",
			CompanyInfo: "Appier 是一家以 AI 為核心的 SaaS 跨國科技公司，專注於 MarTech 與 AI 決策解決方案。",
			Tags:        []string{"AI", "MarTech", "SaaS", "Engineering"},
			MetContext:  "2026 Cloud Summit Taipei",
			MetDate:     lastWeek,
			CreatedAt:   lastWeek,
			UpdatedAt:   lastWeek,
		},
		{
			ID:          "card-seed-3",
			UserID:      "demo",
			Name:        "張志豪 (Howard Chang)",
			Title:       "Director of IC Design",
			Company:     "台積電 (TSMC)",
			Email:       "howard.chang@tsmc.example.com",
			Phone:       "+886-933-111-222",
			Address:     "新竹科學園區力行六路8號",
			Website:     "https://www.tsmc.com",
			RawOCR:      "TSMC 台積電 | 張志豪 Howard Chang | Director of IC Design | howard.chang@tsmc.example.com | 0933-111-222",
			CompanyInfo: "台灣積體電路製造股份有限公司 (TSMC) 為全球最大的專業積體電路製造服務公司，提供先進半導體製程。",
			Tags:        []string{"Semiconductor", "IC Design", "Hardware", "AI Chip"},
			MetContext:  "SEMICON Taiwan 2026",
			MetDate:     twoWeeksAgo,
			CreatedAt:   twoWeeksAgo,
			UpdatedAt:   twoWeeksAgo,
		},
		{
			ID:          "card-seed-4",
			UserID:      "demo",
			Name:        "王心怡 (Cindy Wang)",
			Title:       "Partner & FinTech Lead",
			Company:     "國泰金控 (Cathay Financial Holding)",
			Email:       "cindy.wang@cathay.example.com",
			Phone:       "+886-955-666-777",
			Address:     "台北市大安區仁愛路四段296號",
			Website:     "https://www.cathayholdings.com",
			RawOCR:      "國泰金控 Cathay | 王心怡 Cindy Wang | Partner & FinTech Lead | cindy.wang@cathay.example.com | 0955-666-777",
			CompanyInfo: "國泰金融控股股份有限公司為台灣領先的大型金融機構，涵蓋人壽、銀行、產險與數位金融創新服務。",
			Tags:        []string{"FinTech", "Finance", "Banking", "Enterprise"},
			MetContext:  "FinTech Taipei 晚宴",
			MetDate:     lastMonth,
			CreatedAt:   lastMonth,
			UpdatedAt:   lastMonth,
		},
	}

	for _, c := range seeded {
		if c.CompanySummary == "" {
			c.CompanySummary = c.CompanyInfo
		}
		s.cards[c.ID] = c
	}
}

// NormalizePhone normalizes Taiwan phone numbers into E.164 format (+886-9xx-xxx-xxx or +886-x-xxxx-xxxx).
func NormalizePhone(phone string) string {
	trimmed := strings.TrimSpace(phone)
	if trimmed == "" {
		return ""
	}
	digitsOnly := regexp.MustCompile(`[^\d+]`).ReplaceAllString(trimmed, "")
	if strings.HasPrefix(digitsOnly, "09") && len(digitsOnly) == 10 {
		return "+886-" + digitsOnly[1:4] + "-" + digitsOnly[4:7] + "-" + digitsOnly[7:10]
	}
	if strings.HasPrefix(digitsOnly, "+8869") && len(digitsOnly) == 13 {
		return "+886-" + digitsOnly[4:7] + "-" + digitsOnly[7:10] + "-" + digitsOnly[10:13]
	}
	if strings.HasPrefix(digitsOnly, "8869") && len(digitsOnly) == 12 {
		return "+886-" + digitsOnly[3:6] + "-" + digitsOnly[6:9] + "-" + digitsOnly[9:12]
	}
	return trimmed
}

// Save inserts or updates a BusinessCard in the store.
func (s *MemoryStore) Save(_ context.Context, card *models.BusinessCard) error {
	if card == nil {
		return ErrCardNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cp := card.Clone()
	if cp.ID == "" {
		cp.ID = uuid.NewString()
	}
	if cp.Phone != "" {
		cp.Phone = NormalizePhone(cp.Phone)
	}
	if cp.CompanySummary == "" && cp.CompanyInfo != "" {
		cp.CompanySummary = cp.CompanyInfo
	} else if cp.CompanyInfo == "" && cp.CompanySummary != "" {
		cp.CompanyInfo = cp.CompanySummary
	}
	if cp.CreatedAt.IsZero() {
		if existing, ok := s.cards[cp.ID]; ok && !existing.CreatedAt.IsZero() {
			cp.CreatedAt = existing.CreatedAt
		} else {
			cp.CreatedAt = now
		}
	}
	if cp.MetDate.IsZero() {
		cp.MetDate = now
	}
	cp.UpdatedAt = now

	s.cards[cp.ID] = cp
	// Propagate generated ID and timestamps back to caller's struct.
	*card = *cp.Clone()
	return nil
}

// GetByID retrieves a BusinessCard by its ID.
func (s *MemoryStore) GetByID(_ context.Context, id string) (*models.BusinessCard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, ok := s.cards[id]
	if !ok {
		return nil, ErrCardNotFound
	}
	return c.Clone(), nil
}

// CheckDuplicate checks whether an existing card matches (Name + Company) or Email for the user.
func (s *MemoryStore) CheckDuplicate(_ context.Context, userID, name, company, email string) (*models.BusinessCard, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	normName := strings.ToLower(strings.TrimSpace(name))
	normCompany := strings.ToLower(strings.TrimSpace(company))
	normEmail := strings.ToLower(strings.TrimSpace(email))

	for _, c := range s.cards {
		if userID != "" && c.UserID != "" && c.UserID != "demo" && c.UserID != userID {
			continue
		}
		cEmail := strings.ToLower(strings.TrimSpace(c.Email))
		if normEmail != "" && cEmail != "" && normEmail == cEmail {
			return c.Clone(), true, nil
		}
		cName := strings.ToLower(strings.TrimSpace(c.Name))
		cCompany := strings.ToLower(strings.TrimSpace(c.Company))
		if normName != "" && normCompany != "" && cName != "" && cCompany != "" {
			if (cName == normName || strings.Contains(cName, normName) || strings.Contains(normName, cName)) &&
				(cCompany == normCompany || strings.Contains(cCompany, normCompany) || strings.Contains(normCompany, cCompany)) {
				return c.Clone(), true, nil
			}
		}
	}
	return nil, false, nil
}

// HybridSearch performs structured + keyword filtering across stored business cards.
func (s *MemoryStore) HybridSearch(_ context.Context, filter models.SearchFilter) ([]*models.BusinessCard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*models.BusinessCard
	for _, c := range s.cards {
		if filter.UserID != "" && c.UserID != "" && c.UserID != "demo" && c.UserID != filter.UserID {
			continue
		}
		if filter.TargetCardID != "" {
			if c.ID == filter.TargetCardID {
				results = append(results, c.Clone())
			}
			continue
		}
		if !matchesStructuredFilter(c, filter) {
			continue
		}
		if !matchesKeywordsStrict(c, filter.Keywords) {
			continue
		}
		results = append(results, c.Clone())
	}

	sortCardsByTime(results)
	return results, nil
}

// BroadSearch performs a relaxed fuzzy search across any keyword token or returns recent cards.
func (s *MemoryStore) BroadSearch(_ context.Context, userID string, keywords []string) ([]*models.BusinessCard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type scoredCard struct {
		card  *models.BusinessCard
		score int
	}

	var tokens []string
	for _, kw := range keywords {
		for _, part := range strings.Fields(strings.ToLower(strings.TrimSpace(kw))) {
			if part != "" {
				tokens = append(tokens, part)
			}
		}
	}

	var scored []scoredCard
	var allUserCards []*models.BusinessCard

	for _, c := range s.cards {
		if userID != "" && c.UserID != "" && c.UserID != "demo" && c.UserID != userID {
			continue
		}
		cp := c.Clone()
		allUserCards = append(allUserCards, cp)

		if len(tokens) == 0 {
			scored = append(scored, scoredCard{card: cp, score: 1})
			continue
		}

		haystack := cardSearchableText(cp)
		score := 0
		for _, tok := range tokens {
			if strings.Contains(haystack, tok) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredCard{card: cp, score: score})
		}
	}

	if len(scored) > 0 {
		sort.Slice(scored, func(i, j int) bool {
			if scored[i].score != scored[j].score {
				return scored[i].score > scored[j].score
			}
			return scored[i].card.UpdatedAt.After(scored[j].card.UpdatedAt)
		})
		out := make([]*models.BusinessCard, 0, len(scored))
		for _, sc := range scored {
			out = append(out, sc.card)
		}
		return out, nil
	}

	sortCardsByTime(allUserCards)
	return allUserCards, nil
}

// ListByUser returns all cards visible to the given userID sorted by most recently updated.
func (s *MemoryStore) ListByUser(_ context.Context, userID string) ([]*models.BusinessCard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*models.BusinessCard
	for _, c := range s.cards {
		if userID != "" && c.UserID != "" && c.UserID != "demo" && c.UserID != userID {
			continue
		}
		results = append(results, c.Clone())
	}
	sortCardsByTime(results)
	return results, nil
}

func matchesStructuredFilter(c *models.BusinessCard, filter models.SearchFilter) bool {
	if filter.Company != "" {
		q := strings.ToLower(strings.TrimSpace(filter.Company))
		if !strings.Contains(strings.ToLower(c.Company), q) &&
			!strings.Contains(strings.ToLower(c.CompanyInfo), q) &&
			!strings.Contains(strings.ToLower(c.CompanySummary), q) {
			return false
		}
	}
	if filter.Title != "" {
		q := strings.ToLower(strings.TrimSpace(filter.Title))
		if !strings.Contains(strings.ToLower(c.Title), q) {
			return false
		}
	}
	if filter.RoleFilter != "" {
		q := strings.ToLower(strings.TrimSpace(filter.RoleFilter))
		if !strings.Contains(strings.ToLower(c.Title), q) {
			return false
		}
	}
	if filter.ContextFilter != "" {
		q := strings.ToLower(strings.TrimSpace(filter.ContextFilter))
		if !strings.Contains(strings.ToLower(c.MetContext), q) {
			return false
		}
	}
	if filter.Tag != "" {
		q := strings.ToLower(strings.TrimSpace(filter.Tag))
		matchedTag := false
		for _, t := range c.Tags {
			if strings.Contains(strings.ToLower(t), q) || strings.Contains(q, strings.ToLower(t)) {
				matchedTag = true
				break
			}
		}
		if !matchedTag &&
			!strings.Contains(strings.ToLower(c.CompanyInfo), q) &&
			!strings.Contains(strings.ToLower(c.CompanySummary), q) &&
			!strings.Contains(strings.ToLower(c.Title), q) {
			return false
		}
	}
	if filter.DateRange != nil {
		if !filter.DateRange.Start.IsZero() && c.MetDate.Before(filter.DateRange.Start) {
			return false
		}
		if !filter.DateRange.End.IsZero() && c.MetDate.After(filter.DateRange.End) {
			return false
		}
	}
	return true
}

func matchesKeywordsStrict(c *models.BusinessCard, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	haystack := cardSearchableText(c)
	for _, kw := range keywords {
		q := strings.ToLower(strings.TrimSpace(kw))
		if q == "" {
			continue
		}
		if !strings.Contains(haystack, q) {
			return false
		}
	}
	return true
}

func cardSearchableText(c *models.BusinessCard) string {
	parts := []string{
		c.Name,
		c.Title,
		c.Company,
		c.Email,
		c.Phone,
		c.Address,
		c.TaxID,
		c.Website,
		c.RawOCR,
		c.CompanySummary,
		c.CompanyInfo,
		c.MetContext,
		strings.Join(c.Tags, " "),
	}
	return strings.ToLower(strings.Join(parts, " | "))
}

func sortCardsByTime(cards []*models.BusinessCard) {
	sort.Slice(cards, func(i, j int) bool {
		if !cards[i].UpdatedAt.Equal(cards[j].UpdatedAt) {
			return cards[i].UpdatedAt.After(cards[j].UpdatedAt)
		}
		return cards[i].ID < cards[j].ID
	})
}
