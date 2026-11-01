package campaign

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// LegalNoticeDeliveryRunDefinition carries source-owned immutable facts into
// Campaign's sealed email-delivery history boundary.
type LegalNoticeDeliveryRunDefinition struct {
	TermsID                 *string
	PrivacyID               *string
	ScheduledAt             time.Time
	TemplateEventKey        string
	TemplateData            map[string]string
	Snapshot                CampaignDeliverySnapshot
	SourceTemplateID        string
	SourceLayoutID          *string
	SourceTemplateUpdatedAt time.Time
	SourceLayoutUpdatedAt   *time.Time
	SourceTermsVersion      *int32
	SourcePrivacyVersion    *int32
}

func ValidateLegalNoticeDeliveryTemplateData(
	eventKey string,
	values map[string]string,
) error {
	_, err := newCampaignDeliveryTemplateData(
		EmailDeliveryRunKindLegalNotice,
		strings.TrimSpace(eventKey),
		values,
	)
	return err
}

// SealLegalNoticeDeliveryRun persists the immutable Legal source and Email
// Authoring snapshots in Campaign-owned delivery history.
func SealLegalNoticeDeliveryRun(
	ctx context.Context,
	tx *gorm.DB,
	definition LegalNoticeDeliveryRunDefinition,
) (*model.CampaignDeliveryRun, error) {
	eventKey := strings.TrimSpace(definition.TemplateEventKey)
	if eventKey == "" {
		return nil, domainerr.Required("template_event_key")
	}
	if err := validateLegalNoticeDeliveryAuthority(definition); err != nil {
		return nil, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	if err := ValidateCampaignDeliverySnapshot(definition.Snapshot); err != nil {
		return nil, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	templateData, err := newCampaignDeliveryTemplateData(
		EmailDeliveryRunKindLegalNotice,
		eventKey,
		definition.TemplateData,
	)
	if err != nil {
		return nil, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	renderSnapshot, err := campaignDeliverySnapshotJSONFields(definition.Snapshot)
	if err != nil {
		return nil, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	sealedTemplateData := model.JSONFields{}
	if templateData.PolicyTitle != nil {
		sealedTemplateData["policy_title"] = *templateData.PolicyTitle
	}
	if templateData.EffectiveDate != nil {
		sealedTemplateData["effective_date"] = *templateData.EffectiveDate
	}
	if templateData.PreviewURL != nil {
		sealedTemplateData["preview_url"] = *templateData.PreviewURL
	}
	if templateData.TermsURL != nil {
		sealedTemplateData["terms_url"] = *templateData.TermsURL
	}
	if templateData.PrivacyURL != nil {
		sealedTemplateData["privacy_url"] = *templateData.PrivacyURL
	}
	templateID := strings.TrimSpace(definition.SourceTemplateID)
	templateUpdatedAt := definition.SourceTemplateUpdatedAt.UTC()
	run := &model.CampaignDeliveryRun{
		RunKind:                 EmailDeliveryRunKindLegalNotice,
		TermsID:                 definition.TermsID,
		PrivacyID:               definition.PrivacyID,
		Status:                  CampaignDeliveryRunStatusScheduled,
		ScheduledAt:             definition.ScheduledAt.UTC(),
		TemplateEventKey:        &eventKey,
		TemplateData:            sealedTemplateData,
		RenderSnapshot:          renderSnapshot,
		SnapshotSchemaVersion:   CampaignDeliverySnapshotSchemaVersion,
		SourceTemplateID:        &templateID,
		SourceLayoutID:          definition.SourceLayoutID,
		SourceTemplateUpdatedAt: &templateUpdatedAt,
		SourceLayoutUpdatedAt:   definition.SourceLayoutUpdatedAt,
		SourceTermsVersion:      definition.SourceTermsVersion,
		SourcePrivacyVersion:    definition.SourcePrivacyVersion,
		TargetQueryVersion:      CampaignDeliveryTargetQueryVersion,
		TargetMode:              CampaignDeliveryTargetModeAllUsers,
		TargetRecipientScope:    campaignRecipientScopeAllMatchingUsers,
	}
	if err := sealCampaignOwnedEmailDeliveryRun(ctx, tx, run, nil); err != nil {
		return nil, err
	}
	return run, nil
}

func validateLegalNoticeDeliveryAuthority(
	definition LegalNoticeDeliveryRunDefinition,
) error {
	termsID := strings.TrimSpace(ptrStringValue(definition.TermsID))
	privacyID := strings.TrimSpace(ptrStringValue(definition.PrivacyID))
	if (termsID == "") == (privacyID == "") {
		return domainerr.New(domainerr.Invalid, "", "legal notice delivery requires exactly one policy reference")
	}
	if termsID != "" {
		if definition.SourceTermsVersion == nil || definition.SourcePrivacyVersion != nil {
			return domainerr.New(domainerr.Invalid, "", "terms delivery source version authority is invalid")
		}
	} else if definition.SourcePrivacyVersion == nil || definition.SourceTermsVersion != nil {
		return domainerr.New(domainerr.Invalid, "", "privacy delivery source version authority is invalid")
	}
	if strings.TrimSpace(definition.SourceTemplateID) == "" || definition.SourceTemplateUpdatedAt.IsZero() {
		return domainerr.New(domainerr.Invalid, "", "legal notice email template source revision is required")
	}
	if definition.SourceLayoutID == nil && definition.SourceLayoutUpdatedAt != nil ||
		definition.SourceLayoutID != nil && definition.SourceLayoutUpdatedAt == nil {
		return domainerr.New(domainerr.Invalid, "", "legal notice email layout identity and revision must appear together")
	}
	return nil
}
