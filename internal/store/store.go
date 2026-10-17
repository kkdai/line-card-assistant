package store

import (
	"context"
	"errors"

	"github.com/kkdai/line-card-assistant/internal/models"
)

// ErrCardNotFound is returned when a requested business card does not exist in the store.
var ErrCardNotFound = errors.New("business card not found")

// CardStore defines the persistence and search interface for business cards.
type CardStore interface {
	Save(ctx context.Context, card *models.BusinessCard) error
	GetByID(ctx context.Context, id string) (*models.BusinessCard, error)
	CheckDuplicate(ctx context.Context, userID, name, company, email string) (*models.BusinessCard, bool, error)
	HybridSearch(ctx context.Context, filter models.SearchFilter) ([]*models.BusinessCard, error)
	ListByUser(ctx context.Context, userID string) ([]*models.BusinessCard, error)
}

// CardRepository is an alias for CardStore.
type CardRepository = CardStore
