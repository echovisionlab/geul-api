package legal

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const legalNoticeLeadTime = 7 * 24 * time.Hour

func legalNoticeDispatchAt(now time.Time, effectiveAt time.Time) time.Time {
	dispatchAt := effectiveAt.UTC().Add(-legalNoticeLeadTime)
	if dispatchAt.Before(now.UTC()) {
		return now.UTC()
	}
	return dispatchAt
}

func newAutomaticLegalNoticePreviewURL(baseURL string) (string, error) {
	token, err := crypto.GenerateSecureToken()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/s/" + token, nil
}

func automaticLegalNoticePreviewToken(previewURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(previewURL))
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "s" {
		return "", false
	}
	token, err := url.PathUnescape(parts[1])
	if err != nil || strings.TrimSpace(token) == "" || strings.Contains(token, "/") {
		return "", false
	}
	return token, true
}

func legalNoticePreviewReference(run model.CampaignDeliveryRun) (string, string, string, bool) {
	eventKey := strings.TrimSpace(ptrStringValue(run.TemplateEventKey))
	switch eventKey {
	case "privacy_update":
		return "privacy_history", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(), strings.TrimSpace(ptrStringValue(run.PrivacyID)), true
	case "terms_update":
		return "terms_history", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(), strings.TrimSpace(ptrStringValue(run.TermsID)), true
	default:
		return "", "", "", false
	}
}

// PrepareAutomaticNoticePreviewShareLink materializes the bounded share link
// from a sealed legal-notice run immediately before Campaign dispatch.
func PrepareAutomaticNoticePreviewShareLink(
	ctx context.Context,
	tx *gorm.DB,
	run model.CampaignDeliveryRun,
	now time.Time,
) error {
	tableName, entityType, entityID, relevant := legalNoticePreviewReference(run)
	if !relevant {
		return nil
	}
	if entityID == "" {
		return fmt.Errorf("legal update delivery run is missing its policy reference")
	}
	templateData, err := decodeEmailDeliveryTemplateData(run.RunKind, ptrStringValue(run.TemplateEventKey), run.TemplateData)
	if err != nil {
		return err
	}
	if templateData.PreviewURL == nil {
		return fmt.Errorf("legal update delivery run is missing preview_url")
	}
	token, ok := automaticLegalNoticePreviewToken(*templateData.PreviewURL)
	if !ok {
		return fmt.Errorf("legal update delivery run has an invalid automatic preview_url")
	}

	var policy struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
	}
	result := tx.WithContext(ctx).
		Table(tableName).
		Select("status", "effective_from").
		Where("id = ?", entityID).
		Take(&policy)
	if result.Error != nil {
		return result.Error
	}
	if policy.EffectiveFrom == nil || !policy.EffectiveFrom.After(now) {
		return fmt.Errorf("legal update preview is no longer available before its effective time")
	}
	expectedStatus := managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String()
	if entityType == managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String() {
		expectedStatus = managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String()
	}
	if policy.Status != expectedStatus {
		return fmt.Errorf("legal update preview policy is no longer scheduled")
	}

	createdAt := now.UTC()
	expiresAt := policy.EffectiveFrom.UTC()
	link := model.ShareLink{
		Token:      token,
		EntityType: entityType,
		EntityID:   entityID,
		ExpiresAt:  &expiresAt,
		CreatedAt:  createdAt,
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "token"}}, DoNothing: true}).
		Create(&link).Error; err != nil {
		return err
	}

	var persisted model.ShareLink
	if err := tx.WithContext(ctx).Where("token = ?", token).Take(&persisted).Error; err != nil {
		return err
	}
	if persisted.EntityType != entityType || persisted.EntityID != entityID ||
		persisted.PasswordHash != nil || persisted.ExpiresAt == nil ||
		!persisted.ExpiresAt.Equal(expiresAt) {
		return fmt.Errorf("automatic legal preview token is already bound to a different share link")
	}
	return nil
}

func deleteAutomaticLegalNoticePreviewShareLinks(
	ctx context.Context,
	tx *gorm.DB,
	referenceType string,
	referenceID string,
) error {
	referenceColumn := ""
	eventKey := ""
	entityType := ""
	switch referenceType {
	case EmailDeliveryReferenceTypePrivacy:
		referenceColumn = "privacy_id"
		eventKey = "privacy_update"
		entityType = managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String()
	case EmailDeliveryReferenceTypeTerms:
		referenceColumn = "terms_id"
		eventKey = "terms_update"
		entityType = managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String()
	default:
		return fmt.Errorf("unsupported legal notice reference type %q", referenceType)
	}

	var runs []model.CampaignDeliveryRun
	if err := tx.WithContext(ctx).
		Select("id", "run_kind", "template_event_key", "template_data", "privacy_id", "terms_id").
		Where(referenceColumn+" = ? AND template_event_key = ?", referenceID, eventKey).
		Order("id ASC").
		Find(&runs).Error; err != nil {
		return err
	}
	for i := range runs {
		data, err := decodeEmailDeliveryTemplateData(runs[i].RunKind, eventKey, runs[i].TemplateData)
		if err != nil || data.PreviewURL == nil {
			continue
		}
		token, ok := automaticLegalNoticePreviewToken(*data.PreviewURL)
		if !ok {
			continue
		}
		if err := tx.WithContext(ctx).
			Where("token = ? AND entity_type = ? AND entity_id = ? AND password_hash IS NULL", token, entityType, referenceID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
	}
	return nil
}
