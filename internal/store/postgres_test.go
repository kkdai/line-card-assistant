package store

import (
	"context"
	"testing"
	"time"

	"github.com/kkdai/line-card-assistant/internal/models"
)

func TestBusinessCardRecordRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	original := &models.BusinessCard{
		ID:             "card-pg-1",
		UserID:         "U_PG_USER",
		Name:           "林雅婷 (Yating Lin)",
		Title:          "Chief AI Architect",
		Company:        "Google Cloud Taiwan",
		Email:          "yating.lin@example.com",
		Phone:          "+886-912-345-678",
		Address:        "台北市信義區信義路五段7號",
		TaxID:          "12345678",
		Website:        "https://cloud.google.com",
		RawOCR:         "Google Cloud | Yating Lin",
		CompanySummary: "Cloud & AI leader",
		CompanyInfo:    "Cloud & AI leader in Taiwan",
		Tags:           []string{"AI", "Cloud", "Vertex AI"},
		MetContext:     "Google Cloud Summit 2026",
		RawImageURL:    "artifact://business_card.jpg#v1",
		MetDate:        now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	rec, err := FromBusinessCard(original)
	if err != nil {
		t.Fatalf("FromBusinessCard failed: %v", err)
	}
	if rec.TableName() != "business_cards" {
		t.Fatalf("unexpected table name: %s", rec.TableName())
	}

	restored := rec.ToBusinessCard()
	if restored.ID != original.ID || restored.Name != original.Name || restored.Company != original.Company {
		t.Fatalf("restored card mismatch: %+v", restored)
	}
	if len(restored.Tags) != 3 || restored.Tags[2] != "Vertex AI" {
		t.Fatalf("restored tags mismatch: %+v", restored.Tags)
	}
	if restored.RawImageURL != "artifact://business_card.jpg#v1" {
		t.Fatalf("restored RawImageURL mismatch: %q", restored.RawImageURL)
	}
}

func TestBuildPostgresDSNFromEnv(t *testing.T) {
	t.Setenv("CARD_STORE_DSN", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_DSN", "")
	t.Setenv("INSTANCE_CONNECTION_NAME", "")
	t.Setenv("DB_HOST", "")
	t.Setenv("DB_PASS", "")

	if dsn := BuildPostgresDSNFromEnv(); dsn != "" {
		t.Fatalf("expected empty DSN when env vars are unset, got %q", dsn)
	}

	// Test Cloud SQL Unix socket format
	t.Setenv("INSTANCE_CONNECTION_NAME", "my-project:asia-east1:cards-pg")
	t.Setenv("DB_USER", "card_user")
	t.Setenv("DB_PASS", "secret123")
	t.Setenv("DB_NAME", "cards_db")

	gotCloudSQL := BuildPostgresDSNFromEnv()
	wantCloudSQL := "host=/cloudsql/my-project:asia-east1:cards-pg user=card_user password=secret123 dbname=cards_db sslmode=disable"
	if gotCloudSQL != wantCloudSQL {
		t.Fatalf("Cloud SQL DSN mismatch:\ngot:  %q\nwant: %q", gotCloudSQL, wantCloudSQL)
	}

	// Test TCP host format when INSTANCE_CONNECTION_NAME is unset
	t.Setenv("INSTANCE_CONNECTION_NAME", "")
	t.Setenv("DB_HOST", "127.0.0.1")
	t.Setenv("DB_PORT", "5432")
	gotTCP := BuildPostgresDSNFromEnv()
	wantTCP := "host=127.0.0.1 port=5432 user=card_user password=secret123 dbname=cards_db sslmode=disable"
	if gotTCP != wantTCP {
		t.Fatalf("TCP DSN mismatch:\ngot:  %q\nwant: %q", gotTCP, wantTCP)
	}

	// Test explicit DATABASE_URL override
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/mydb?sslmode=disable")
	if got := BuildPostgresDSNFromEnv(); got != "postgres://user:pass@localhost:5432/mydb?sslmode=disable" {
		t.Fatalf("DATABASE_URL override mismatch: %q", got)
	}
}

func TestNewStoreFactoryFallback(t *testing.T) {
	t.Setenv("CARD_STORE_TYPE", "")
	t.Setenv("CARD_STORE_DSN", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_DSN", "")
	t.Setenv("INSTANCE_CONNECTION_NAME", "")
	t.Setenv("DB_HOST", "")
	t.Setenv("DB_PASS", "")

	ctx := context.Background()
	cs, err := NewStore(ctx)
	if err != nil {
		t.Fatalf("expected NewStore to succeed with default MemoryStore, got error: %v", err)
	}
	cards, err := cs.ListByUser(ctx, DefaultDemoUserID)
	if err != nil {
		t.Fatalf("ListByUser failed: %v", err)
	}
	if len(cards) == 0 {
		t.Fatalf("expected seeded demo cards in default MemoryStore")
	}

	// Explicit CARD_STORE_TYPE=cloud_sql without DSN should return an error
	t.Setenv("CARD_STORE_TYPE", "cloud_sql")
	if _, err := NewStore(ctx); err == nil {
		t.Fatalf("expected error when CARD_STORE_TYPE=cloud_sql without DSN")
	}
}

func TestRankCardsByKeywords(t *testing.T) {
	now := time.Now()
	cards := []*models.BusinessCard{
		{
			ID:        "c1",
			Name:      "Alice Lin",
			Company:   "Google Cloud",
			Title:     "AI Architect",
			Tags:      []string{"AI", "Cloud"},
			UpdatedAt: now.Add(-1 * time.Hour),
		},
		{
			ID:        "c2",
			Name:      "Bob Chen",
			Company:   "TSMC",
			Title:     "IC Designer",
			Tags:      []string{"Semiconductor", "AI Chip"},
			UpdatedAt: now,
		},
	}

	ranked := rankCardsByKeywords(cards, []string{"Google", "AI"})
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked cards, got %d", len(ranked))
	}
	if ranked[0].ID != "c1" {
		t.Fatalf("expected c1 (2 token matches) first, got %s", ranked[0].ID)
	}
}
