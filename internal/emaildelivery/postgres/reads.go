package emaildeliverypostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

func NewDeliveryReadStore(db *gorm.DB) email.DeliveryStore {
	return deliveryReadStore{db: db}
}

type deliveryReadStore struct{ db *gorm.DB }

var _ email.DeliveryStore = deliveryReadStore{}

func (s deliveryReadStore) ActiveSuppressionCount(ctx context.Context, address string) (int64, error) {
	var suppressed int64
	err := s.db.WithContext(ctx).Model(&model.EmailSuppression{}).
		Where("LOWER(email) = ? AND released_at IS NULL", address).
		Count(&suppressed).Error
	return suppressed, err
}

func (s deliveryReadStore) ActiveMailAdapterCount(ctx context.Context) (int64, error) {
	var active int64
	err := s.db.WithContext(ctx).Model(&model.MailAdapter{}).
		Where("is_active = ?", true).
		Count(&active).Error
	return active, err
}

func (s deliveryReadStore) ActiveMailAdapters(ctx context.Context) ([]email.MailAdapter, error) {
	var adapters []model.MailAdapter
	err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("priority ASC, created_at ASC").
		Find(&adapters).Error
	if err != nil {
		return nil, err
	}
	result := make([]email.MailAdapter, len(adapters))
	for i, adapter := range adapters {
		result[i] = MailAdapterFromRow(adapter)
	}
	return result, nil
}
