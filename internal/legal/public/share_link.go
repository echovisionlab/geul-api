package public

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
)

type legalShareLinkAccess struct {
	status        model.LegalPolicyStatus
	publicHistory bool
}

// resolveLegalShareLinkAccess reports which version state a Share Link of
// entityID opens: a published version as public history, or a version
// scheduled for later. An expired, unknown or wrongly protected link opens
// nothing.
func resolveLegalShareLinkAccess(
	ctx context.Context,
	db *gorm.DB,
	kind publicKind,
	token string,
	password string,
	entityID string,
	now time.Time,
) (legalShareLinkAccess, bool, error) {
	opened, err := shareLinkOpens(ctx, db, kind, token, password, entityID, now)
	if err != nil || !opened {
		return legalShareLinkAccess{}, false, err
	}
	var target struct {
		Status        model.LegalPolicyStatus `gorm:"column:status"`
		EffectiveFrom *time.Time              `gorm:"column:effective_from"`
	}
	if err := db.WithContext(ctx).
		Table(kind.table).
		Select("status", "effective_from").
		Where("id = ?", entityID).
		Take(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legalShareLinkAccess{}, false, nil
		}
		return legalShareLinkAccess{}, false, err
	}
	if target.Status == kind.active || target.Status == kind.archived {
		return legalShareLinkAccess{status: target.Status, publicHistory: true}, true, nil
	}
	if target.Status != kind.scheduled || target.EffectiveFrom == nil || !target.EffectiveFrom.After(now) {
		return legalShareLinkAccess{}, false, nil
	}
	return legalShareLinkAccess{status: target.Status}, true, nil
}

// shareLinkOpens reports whether token is an unexpired Share Link of the
// version whose password, if it has one, password matches.
func shareLinkOpens(
	ctx context.Context,
	db *gorm.DB,
	kind publicKind,
	token string,
	password string,
	entityID string,
	now time.Time,
) (bool, error) {
	var link model.ShareLink
	if err := db.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, kind.shareLinkEntityType, entityID, now).
		Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if link.ExpiresAt == nil {
		return false, nil
	}
	if link.PasswordHash == nil {
		return true, nil
	}
	if password == "" {
		return false, nil
	}
	return crypto.NewPasswordHasher(nil).Verify(password, *link.PasswordHash)
}
