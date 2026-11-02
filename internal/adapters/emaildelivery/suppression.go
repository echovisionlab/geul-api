package emaildeliveryadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"gorm.io/gorm"
)

type SuppressionStore struct {
	db *gorm.DB
}

func NewSuppressionStore(db *gorm.DB) *SuppressionStore {
	return &SuppressionStore{db: db}
}

func (s *SuppressionStore) Suppress(
	ctx context.Context,
	request emaildelivery.SuppressionRequest,
) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("email suppression database is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return emaildelivery.SuppressAddress(ctx, emaildeliverypostgres.NewStore(tx), request)
	})
}
