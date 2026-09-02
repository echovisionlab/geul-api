package public

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func IsAutomaticLegalNoticePublicHistoryToken(
	ctx context.Context,
	db *gorm.DB,
	token string,
	entityType managev1.ShareLinkEntityType,
	entityID string,
) (bool, error) {
	referenceColumn := ""
	eventKey := ""
	switch entityType {
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY:
		referenceColumn = "privacy_id"
		eventKey = "privacy_update"
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS:
		referenceColumn = "terms_id"
		eventKey = "terms_update"
	default:
		return false, nil
	}

	var previewURLs []string
	if err := db.WithContext(ctx).
		Table("email_delivery_run").
		Select("template_data ->> 'preview_url'").
		Where("run_kind = ?", "legal_notice").
		Where("definition_sealed = ?", true).
		Where(referenceColumn+" = ?", entityID).
		Where("template_event_key = ?", eventKey).
		Where("template_data ->> 'preview_url' IS NOT NULL").
		Pluck("template_data ->> 'preview_url'", &previewURLs).Error; err != nil {
		return false, err
	}
	for _, previewURL := range previewURLs {
		if legalNoticePreviewURLHasExactToken(previewURL, token) {
			return true, nil
		}
	}
	return false, nil
}

func legalNoticePreviewURLHasExactToken(previewURL string, token string) bool {
	parsed, err := url.Parse(strings.TrimSpace(previewURL))
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "s" {
		return false
	}
	previewToken, err := url.PathUnescape(parts[1])
	return err == nil && previewToken == token
}

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
		if err == gorm.ErrRecordNotFound {
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

	tableName := ""
	scheduledStatus := ""
	activeStatus := ""
	archivedStatus := ""
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
		if err == gorm.ErrRecordNotFound {
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
