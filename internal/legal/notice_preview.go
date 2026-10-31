package legal

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
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

// noticePreviewPolicy returns the kind and version an update notice run
// previews; any other run previews nothing.
func noticePreviewPolicy(run model.CampaignDeliveryRun) (*policyKind, string, bool) {
	eventKey := strings.TrimSpace(ptrStringValue(run.TemplateEventKey))
	for _, kind := range policyKinds {
		if eventKey != kind.updateEvent.String() {
			continue
		}
		reference := run.TermsID
		if kind.kind == Privacy {
			reference = run.PrivacyID
		}
		return kind, strings.TrimSpace(ptrStringValue(reference)), true
	}
	return nil, "", false
}

// PrepareAutomaticNoticePreviewShareLink materializes the bounded share link
// from a sealed legal-notice run immediately before Campaign dispatch.
func PrepareAutomaticNoticePreviewShareLink(
	ctx context.Context,
	tx *gorm.DB,
	links PreviewLinks,
	run model.CampaignDeliveryRun,
	now time.Time,
) error {
	kind, entityID, relevant := noticePreviewPolicy(run)
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
		Status        model.LegalPolicyStatus `gorm:"column:status"`
		EffectiveFrom *time.Time              `gorm:"column:effective_from"`
	}
	result := tx.WithContext(ctx).
		Table(kind.table).
		Select("status", "effective_from").
		Where("id = ?", entityID).
		Take(&policy)
	if result.Error != nil {
		return result.Error
	}
	if policy.EffectiveFrom == nil || !policy.EffectiveFrom.After(now) {
		return fmt.Errorf("legal update preview is no longer available before its effective time")
	}
	if policy.Status != kind.scheduled {
		return fmt.Errorf("legal update preview policy is no longer scheduled")
	}

	return links.CreateAutomaticPreview(ctx, tx, AutomaticPreview{
		Token: token, EntityType: kind.shareLinkEntityType, EntityID: entityID,
		CreatedAt: now.UTC(), ExpiresAt: policy.EffectiveFrom.UTC(),
	})
}

// deleteAutomaticNoticePreviewLinks revokes the preview links the version's
// update notices mailed out.
func (s *Service) deleteAutomaticNoticePreviewLinks(ctx context.Context, tx *gorm.DB, kind *policyKind, policyID string) error {
	runs, err := s.notice.UpdateNoticeRuns(ctx, tx, kind.entity(), policyID)
	if err != nil {
		return err
	}
	for i := range runs {
		data, err := decodeEmailDeliveryTemplateData(runs[i].RunKind, kind.updateEvent.String(), runs[i].TemplateData)
		if err != nil || data.PreviewURL == nil {
			if err != nil {
				slog.ErrorContext(ctx, "Legal notice run data is invalid; skipping its preview revocation", "domain", "legal", "operation", "revoke_notice_preview", "outcome", "failed", "run_id", runs[i].ID, failurecause.Attr(err))
			}
			continue
		}
		token, ok := automaticLegalNoticePreviewToken(*data.PreviewURL)
		if !ok {
			continue
		}
		if err := s.previewLinks.DeleteAutomaticPreview(ctx, tx, token, kind.shareLinkEntityType, policyID); err != nil {
			return err
		}
	}
	return nil
}
