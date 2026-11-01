package legal

import (
	"context"
	"errors"
	"strings"
	"time"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EmailDeliveryDispatcher interface {
	DispatchEmailDeliveryRun(context.Context, string) error
}

// NoticeRuntime seals legal-policy source, email-template, and layout
// revisions in the policy transaction. Campaign's adapter reads and stops the
// runs a policy lifecycle change affects.
type NoticeRuntime struct {
	campaignadapter.LegalNoticeRuns
}

func NewNoticeRuntime() *NoticeRuntime {
	return &NoticeRuntime{}
}

// NoticeDispatch delegates the post-commit dispatch of a sealed notice run
// to Campaign's generic delivery runtime and cancels, on db, the update
// notice an activation made obsolete.
type NoticeDispatch struct {
	db         *gorm.DB
	dispatcher EmailDeliveryDispatcher
}

// NewNoticeDispatch returns the post-commit notice steps over db; a nil
// dispatcher answers every dispatch as unavailable.
func NewNoticeDispatch(db *gorm.DB, dispatcher EmailDeliveryDispatcher) *NoticeDispatch {
	return &NoticeDispatch{db: db, dispatcher: dispatcher}
}

func (d *NoticeDispatch) DispatchRun(ctx context.Context, runID string) error {
	if d == nil || d.dispatcher == nil {
		return errs.DependencyUnavailable("Campaign delivery dispatcher")
	}
	return d.dispatcher.DispatchEmailDeliveryRun(ctx, runID)
}

func (d *NoticeDispatch) CancelUpdateNoticeAfterActivation(ctx context.Context, kind legaldomain.PolicyKind, policyID string, now time.Time) {
	campaignadapter.LegalNoticeRuns{}.CancelUpdateNoticeAfterActivation(ctx, d.db, string(kind), policyID, now)
}

type noticeRenderTranslation = campaign.CampaignDeliverySnapshotTranslation
type noticeLayoutTranslation = campaign.CampaignDeliverySnapshotLayout
type noticeRenderSnapshot = campaign.CampaignDeliverySnapshot

func (r *NoticeRuntime) CreateRun(
	ctx context.Context,
	tx *gorm.DB,
	referenceType, referenceID, eventKey string,
	templateData map[string]string,
	scheduledAt time.Time,
) (*model.CampaignDeliveryRun, error) {
	referenceID = strings.TrimSpace(referenceID)
	eventKey = strings.TrimSpace(eventKey)
	if referenceID == "" {
		return nil, errs.Required("reference_id")
	}
	if eventKey == "" {
		return nil, errs.Required("template_event_key")
	}
	if err := campaign.ValidateLegalNoticeDeliveryTemplateData(eventKey, templateData); err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}

	var termsID, privacyID *string
	var termsVersion, privacyVersion *int32
	switch strings.TrimSpace(referenceType) {
	case legaldomain.EmailDeliveryReferenceTypeTerms:
		var policy model.Terms
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "version").First(&policy, "id = ?", referenceID).Error; err != nil {
			return nil, err
		}
		termsID = &policy.ID
		version := int32(policy.Version)
		termsVersion = &version
	case legaldomain.EmailDeliveryReferenceTypePrivacy:
		var policy model.Privacy
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "version").First(&policy, "id = ?", referenceID).Error; err != nil {
			return nil, err
		}
		privacyID = &policy.ID
		version := int32(policy.Version)
		privacyVersion = &version
	default:
		return nil, errs.InvalidArgumentMsg("unsupported legal notice reference type")
	}

	var template model.EmailTemplate
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("event_key = ? AND is_active = ?", eventKey, true).First(&template).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.FailedPrecondition("active legal notice email template is unavailable")
		}
		return nil, err
	}
	if template.UpdatedAt == nil || template.UpdatedAt.IsZero() {
		return nil, errs.FailedPrecondition("legal notice email template source revision is unavailable")
	}
	templateUpdatedAt := template.UpdatedAt.UTC()

	var layoutID *string
	var layoutUpdatedAt *time.Time
	if template.LayoutID != nil && strings.TrimSpace(*template.LayoutID) != "" {
		trimmedID := strings.TrimSpace(*template.LayoutID)
		var layout model.EmailLayout
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "updated_at").First(&layout, "id = ?", trimmedID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.FailedPrecondition("legal notice email layout no longer exists")
			}
			return nil, err
		}
		layoutID = &layout.ID
		updatedAt := layout.UpdatedAt.UTC()
		layoutUpdatedAt = &updatedAt
	}

	snapshot, err := loadNoticeRenderSnapshot(ctx, tx, template, eventKey)
	if err != nil {
		return nil, err
	}
	run, err := campaign.SealLegalNoticeDeliveryRun(
		ctx,
		campaignpostgres.Bind(tx, campaignpostgres.Ports{}, nil),
		campaign.LegalNoticeDeliveryRunDefinition{
			TermsID:                 termsID,
			PrivacyID:               privacyID,
			ScheduledAt:             scheduledAt,
			TemplateEventKey:        eventKey,
			TemplateData:            templateData,
			Snapshot:                snapshot,
			SourceTemplateID:        template.ID,
			SourceLayoutID:          layoutID,
			SourceTemplateUpdatedAt: templateUpdatedAt,
			SourceLayoutUpdatedAt:   layoutUpdatedAt,
			SourceTermsVersion:      termsVersion,
			SourcePrivacyVersion:    privacyVersion,
		},
	)
	if err != nil {
		return nil, err
	}
	row := campaignpostgres.DeliveryRunToRow(*run)
	return &row, nil
}

func loadNoticeRenderSnapshot(
	ctx context.Context,
	db *gorm.DB,
	template model.EmailTemplate,
	eventKey string,
) (noticeRenderSnapshot, error) {
	if strings.HasPrefix(eventKey, "campaign:") {
		return noticeRenderSnapshot{}, errs.FailedPrecondition("legal notice email template event key is invalid")
	}
	rows, err := emailauthoringadapter.TemplateLocaleContents(ctx, db, template.ID)
	if err != nil {
		return noticeRenderSnapshot{}, err
	}
	if len(rows) == 0 {
		return noticeRenderSnapshot{}, errs.FailedPrecondition("legal notice email template has no source content")
	}
	snapshot := noticeRenderSnapshot{Translations: make([]noticeRenderTranslation, 0, len(rows))}
	for _, row := range rows {
		entry := noticeRenderTranslation{
			Locale:  row.Locale,
			Subject: stringValue(row.Subject), ContentHTML: stringValue(row.ContentHTML),
		}
		if row.IsSourceLocale {
			snapshot.SourceLocale = row.Locale
			snapshot.Subject = entry.Subject
			snapshot.ContentHTML = entry.ContentHTML
		}
		snapshot.Translations = append(snapshot.Translations, entry)
	}
	if strings.TrimSpace(snapshot.SourceLocale) == "" {
		return noticeRenderSnapshot{}, errs.FailedPrecondition("legal notice email template source locale is unavailable")
	}
	if template.LayoutID == nil || strings.TrimSpace(*template.LayoutID) == "" {
		return snapshot, nil
	}
	layoutRows, err := email.LoadLayoutLocaleSnapshots(ctx, emailpostgres.Bind(db), strings.TrimSpace(*template.LayoutID))
	if err != nil {
		return noticeRenderSnapshot{}, err
	}
	if len(layoutRows) == 0 {
		return noticeRenderSnapshot{}, errs.FailedPrecondition("legal notice email layout has no source content")
	}
	layoutTranslations := make([]noticeLayoutTranslation, 0, len(layoutRows))
	layoutSourceLocale := ""
	for _, row := range layoutRows {
		if row.IsSourceLocale {
			layoutSourceLocale = row.Locale
		}
		layoutTranslations = append(layoutTranslations, noticeLayoutTranslation{
			Locale: row.Locale, HTMLContent: stringValue(row.ContentHTML),
		})
	}
	if strings.TrimSpace(layoutSourceLocale) == "" {
		return noticeRenderSnapshot{}, errs.FailedPrecondition("legal notice email layout source locale is unavailable")
	}
	snapshot.LayoutSourceLocale = &layoutSourceLocale
	snapshot.LayoutTranslations = &layoutTranslations
	return snapshot, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var (
	_ legalpostgres.Notices      = (*NoticeRuntime)(nil)
	_ legaldomain.NoticeDispatch = (*NoticeDispatch)(nil)
)
