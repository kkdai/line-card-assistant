package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kkdai/line-card-assistant/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BusinessCardRecord is the GORM database model for persisting a models.BusinessCard
// in PostgreSQL / Cloud SQL.
type BusinessCardRecord struct {
	ID             string    `gorm:"primaryKey;size:128"`
	UserID         string    `gorm:"index;size:128;not null"`
	Name           string    `gorm:"index;size:255"`
	Title          string    `gorm:"size:255"`
	Company        string    `gorm:"index;size:255"`
	Email          string    `gorm:"index;size:255"`
	Phone          string    `gorm:"size:64"`
	Address        string    `gorm:"type:text"`
	TaxID          string    `gorm:"size:32"`
	Website        string    `gorm:"size:512"`
	RawOCR         string    `gorm:"type:text"`
	CompanySummary string    `gorm:"type:text"`
	CompanyInfo    string    `gorm:"type:text"`
	TagsJSON       string    `gorm:"column:tags_json;type:text"`
	MetContext     string    `gorm:"type:text"`
	RawImageURL    string    `gorm:"type:text"`
	MetDate        time.Time `gorm:"index"`
	CreatedAt      time.Time `gorm:"index"`
	UpdatedAt      time.Time `gorm:"index"`
}

// TableName overrides the GORM table name for BusinessCardRecord.
func (BusinessCardRecord) TableName() string {
	return "business_cards"
}

// ToBusinessCard converts a database BusinessCardRecord into a domain models.BusinessCard.
func (r *BusinessCardRecord) ToBusinessCard() *models.BusinessCard {
	if r == nil {
		return nil
	}
	var tags []string
	if strings.TrimSpace(r.TagsJSON) != "" {
		_ = json.Unmarshal([]byte(r.TagsJSON), &tags)
	}
	return &models.BusinessCard{
		ID:             r.ID,
		UserID:         r.UserID,
		Name:           r.Name,
		Title:          r.Title,
		Company:        r.Company,
		Email:          r.Email,
		Phone:          r.Phone,
		Address:        r.Address,
		TaxID:          r.TaxID,
		Website:        r.Website,
		RawOCR:         r.RawOCR,
		CompanySummary: r.CompanySummary,
		CompanyInfo:    r.CompanyInfo,
		Tags:           tags,
		MetContext:     r.MetContext,
		RawImageURL:    r.RawImageURL,
		MetDate:        r.MetDate,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

// FromBusinessCard converts a domain models.BusinessCard into a database BusinessCardRecord.
func FromBusinessCard(card *models.BusinessCard) (*BusinessCardRecord, error) {
	if card == nil {
		return nil, ErrCardNotFound
	}
	tagsBytes, err := json.Marshal(card.Tags)
	if err != nil {
		tagsBytes = []byte("[]")
	}
	return &BusinessCardRecord{
		ID:             card.ID,
		UserID:         card.UserID,
		Name:           card.Name,
		Title:          card.Title,
		Company:        card.Company,
		Email:          card.Email,
		Phone:          card.Phone,
		Address:        card.Address,
		TaxID:          card.TaxID,
		Website:        card.Website,
		RawOCR:         card.RawOCR,
		CompanySummary: card.CompanySummary,
		CompanyInfo:    card.CompanyInfo,
		TagsJSON:       string(tagsBytes),
		MetContext:     card.MetContext,
		RawImageURL:    card.RawImageURL,
		MetDate:        card.MetDate,
		CreatedAt:      card.CreatedAt,
		UpdatedAt:      card.UpdatedAt,
	}, nil
}

// PostgresStore is a Cloud SQL / PostgreSQL implementation of CardStore backed by GORM.
type PostgresStore struct {
	db *gorm.DB
}

// NewPostgresStore connects to PostgreSQL / Cloud SQL using the provided DSN,
// auto-migrates the business_cards table, and seeds demo cards if empty.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres DSN cannot be empty")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("connect to postgres card store: %w", err)
	}
	return NewPostgresStoreFromDB(db, true)
}

// NewPostgresStoreFromDB wraps an existing *gorm.DB connection, auto-migrates the schema,
// and optionally seeds demo cards when the table is empty.
func NewPostgresStoreFromDB(db *gorm.DB, seedDemo bool) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("gorm.DB cannot be nil")
	}
	if err := db.AutoMigrate(&BusinessCardRecord{}); err != nil {
		return nil, fmt.Errorf("auto-migrate business_cards schema: %w", err)
	}
	ps := &PostgresStore{db: db}
	if seedDemo {
		ps.seedDefaultCardsIfEmpty(context.Background())
	}
	return ps, nil
}

// BuildPostgresDSNFromEnv resolves a PostgreSQL / Cloud SQL connection string from environment variables.
// Priority:
//  1. CARD_STORE_DSN / DATABASE_URL / POSTGRES_DSN
//  2. Cloud SQL Unix socket when INSTANCE_CONNECTION_NAME + DB_PASS are set
//  3. Standard TCP host when DB_HOST + DB_PASS are set
func BuildPostgresDSNFromEnv() string {
	for _, key := range []string{"CARD_STORE_DSN", "DATABASE_URL", "POSTGRES_DSN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}

	dbUser := envOrDefault("DB_USER", "postgres")
	dbName := envOrDefault("DB_NAME", "postgres")
	dbPass := strings.TrimSpace(os.Getenv("DB_PASS"))
	instanceConn := strings.TrimSpace(os.Getenv("INSTANCE_CONNECTION_NAME"))
	if instanceConn != "" && dbPass != "" {
		return fmt.Sprintf(
			"host=/cloudsql/%s user=%s password=%s dbname=%s sslmode=disable",
			instanceConn, dbUser, dbPass, dbName,
		)
	}

	dbHost := strings.TrimSpace(os.Getenv("DB_HOST"))
	dbPort := envOrDefault("DB_PORT", "5432")
	if dbHost != "" && dbPass != "" {
		return fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			dbHost, dbPort, dbUser, dbPass, dbName,
		)
	}
	return ""
}

// NewStore creates a CardStore based on environment configuration:
//   - When CARD_STORE_TYPE is "postgres" or "cloud_sql", or when a Postgres DSN / Cloud SQL
//     connection is configured via environment variables, it initializes PostgresStore.
//   - Otherwise (or if CARD_STORE_TYPE="memory"), it returns an in-memory MemoryStore.
func NewStore(_ context.Context) (CardStore, error) {
	storeType := strings.ToLower(strings.TrimSpace(os.Getenv("CARD_STORE_TYPE")))
	if storeType == "memory" || storeType == "in_memory" {
		return NewMemoryStore(), nil
	}

	dsn := BuildPostgresDSNFromEnv()
	if storeType == "postgres" || storeType == "cloud_sql" || storeType == "postgresql" {
		if dsn == "" {
			return nil, fmt.Errorf("CARD_STORE_TYPE=%q requires DATABASE_URL or Cloud SQL env vars (INSTANCE_CONNECTION_NAME, DB_PASS)", storeType)
		}
		return NewPostgresStore(dsn)
	}

	if dsn != "" {
		ps, err := NewPostgresStore(dsn)
		if err != nil {
			slog.Warn("failed to connect to PostgresStore from env; falling back to MemoryStore", "err", err)
			return NewMemoryStore(), err
		}
		return ps, nil
	}

	return NewMemoryStore(), nil
}

func (s *PostgresStore) seedDefaultCardsIfEmpty(ctx context.Context) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&BusinessCardRecord{}).Count(&count).Error; err != nil || count > 0 {
		return
	}
	mem := NewMemoryStore()
	cards, err := mem.ListByUser(ctx, DefaultDemoUserID)
	if err != nil {
		return
	}
	for _, c := range cards {
		_ = s.Save(ctx, c)
	}
}

// Save inserts or updates a BusinessCard in PostgreSQL / Cloud SQL.
func (s *PostgresStore) Save(ctx context.Context, card *models.BusinessCard) error {
	if card == nil {
		return ErrCardNotFound
	}
	now := time.Now()
	cp := card.Clone()
	if cp.ID == "" {
		cp.ID = uuid.NewString()
	}
	if cp.UserID == "" {
		cp.UserID = DefaultDemoUserID
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
		if existing, err := s.GetByID(ctx, cp.ID); err == nil && existing != nil && !existing.CreatedAt.IsZero() {
			cp.CreatedAt = existing.CreatedAt
		} else {
			cp.CreatedAt = now
		}
	}
	if cp.MetDate.IsZero() {
		cp.MetDate = now
	}
	cp.UpdatedAt = now

	rec, err := FromBusinessCard(cp)
	if err != nil {
		return err
	}

	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(rec).Error; err != nil {
		return fmt.Errorf("upsert business card %q: %w", cp.ID, err)
	}

	*card = *cp.Clone()
	return nil
}

// GetByID retrieves a BusinessCard by its primary key ID.
func (s *PostgresStore) GetByID(ctx context.Context, id string) (*models.BusinessCard, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrCardNotFound
	}
	var rec BusinessCardRecord
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCardNotFound
		}
		return nil, fmt.Errorf("get business card %q: %w", id, err)
	}
	return rec.ToBusinessCard(), nil
}

// CheckDuplicate checks whether an existing card matches (Name + Company) or Email for the user.
func (s *PostgresStore) CheckDuplicate(ctx context.Context, userID, name, company, email string) (*models.BusinessCard, bool, error) {
	cards, err := s.ListByUser(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	normName := strings.ToLower(strings.TrimSpace(name))
	normCompany := strings.ToLower(strings.TrimSpace(company))
	normEmail := strings.ToLower(strings.TrimSpace(email))

	for _, c := range cards {
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

// HybridSearch executes structured + keyword filtering across stored business cards for the user.
func (s *PostgresStore) HybridSearch(ctx context.Context, filter models.SearchFilter) ([]*models.BusinessCard, error) {
	if filter.TargetCardID != "" {
		c, err := s.GetByID(ctx, filter.TargetCardID)
		if err != nil {
			if errors.Is(err, ErrCardNotFound) {
				return nil, nil
			}
			return nil, err
		}
		if filter.UserID != "" && c.UserID != "" && c.UserID != DefaultDemoUserID && c.UserID != filter.UserID {
			return nil, nil
		}
		return []*models.BusinessCard{c}, nil
	}

	cards, err := s.ListByUser(ctx, filter.UserID)
	if err != nil {
		return nil, err
	}

	var results []*models.BusinessCard
	for _, c := range cards {
		if !matchesStructuredFilter(c, filter) {
			continue
		}
		if !matchesKeywordsStrict(c, filter.Keywords) {
			continue
		}
		results = append(results, c)
	}
	sortCardsByTime(results)
	return results, nil
}

// BroadSearch performs a relaxed fuzzy token-scored search across the user's business cards.
func (s *PostgresStore) BroadSearch(ctx context.Context, userID string, keywords []string) ([]*models.BusinessCard, error) {
	cards, err := s.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return rankCardsByKeywords(cards, keywords), nil
}

// ListByUser returns all cards visible to the given userID (including shared "demo" cards)
// ordered by most recently updated.
func (s *PostgresStore) ListByUser(ctx context.Context, userID string) ([]*models.BusinessCard, error) {
	query := s.db.WithContext(ctx).Model(&BusinessCardRecord{})
	trimmedUser := strings.TrimSpace(userID)
	if trimmedUser != "" {
		query = query.Where("user_id = ? OR user_id = ? OR user_id = ''", trimmedUser, DefaultDemoUserID)
	}

	var records []BusinessCardRecord
	if err := query.Order("updated_at DESC, id ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list business cards for user %q: %w", userID, err)
	}

	out := make([]*models.BusinessCard, 0, len(records))
	for i := range records {
		out = append(out, records[i].ToBusinessCard())
	}
	sortCardsByTime(out)
	return out, nil
}

func rankCardsByKeywords(cards []*models.BusinessCard, keywords []string) []*models.BusinessCard {
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

	if len(tokens) == 0 {
		sortCardsByTime(cards)
		return cards
	}

	var scored []scoredCard
	for _, c := range cards {
		haystack := cardSearchableText(c)
		score := 0
		for _, tok := range tokens {
			if strings.Contains(haystack, tok) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredCard{card: c, score: score})
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
		return out
	}

	sortCardsByTime(cards)
	return cards
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
