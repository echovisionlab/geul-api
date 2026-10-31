package public

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type legalShareLinkAccess struct {
	status        string
	publicHistory bool
}

func resolveLegalShareLinkAccess(
	ctx context.Context,
	db *gorm.DB,
	token string,
	password string,
	entityType managev1.ShareLinkEntityType,
	entityID string,
	now time.Time,
) (legalShareLinkAccess, bool, error) {
	var link model.ShareLink
	if err := db.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, entityType.String(), entityID, now).
		Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legalShareLinkAccess{}, false, nil
		}
		return legalShareLinkAccess{}, false, err
	}
	if link.ExpiresAt == nil {
		return legalShareLinkAccess{}, false, nil
	}
	if link.PasswordHash != nil {
		if password == "" {
			return legalShareLinkAccess{}, false, nil
		}
		matched, err := crypto.NewPasswordHasher(nil).Verify(password, *link.PasswordHash)
		if err != nil {
			return legalShareLinkAccess{}, false, err
		}
		if !matched {
			return legalShareLinkAccess{}, false, nil
		}
	}

	var tableName string
	var scheduledStatus string
	var activeStatus string
	var archivedStatus string
	switch entityType {
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY:
		tableName = "privacy_history"
		scheduledStatus = managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String()
		activeStatus = managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String()
		archivedStatus = managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS:
		tableName = "terms_history"
		scheduledStatus = managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String()
		activeStatus = managev1.TermsStatus_TERMS_STATUS_ACTIVE.String()
		archivedStatus = managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()
	default:
		return legalShareLinkAccess{}, false, nil
	}

	var target struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
	}
	if err := db.WithContext(ctx).
		Table(tableName).
		Select("status", "effective_from").
		Where("id = ?", entityID).
		Take(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legalShareLinkAccess{}, false, nil
		}
		return legalShareLinkAccess{}, false, err
	}
	if target.Status == activeStatus || target.Status == archivedStatus {
		return legalShareLinkAccess{status: target.Status, publicHistory: true}, true, nil
	}
	if target.Status != scheduledStatus || target.EffectiveFrom == nil || !target.EffectiveFrom.After(now) {
		return legalShareLinkAccess{}, false, nil
	}
	return legalShareLinkAccess{status: target.Status}, true, nil
}
