package emaildeliveryadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
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
	var referenceID *string
	if value := strings.TrimSpace(request.ReferenceID); value != "" {
		referenceID = &value
	}
	return emaildelivery.SuppressEmailAddress(
		ctx, s.db, request.Email, request.Reason, request.Source,
		referenceID, request.ErrorType,
	)
}
