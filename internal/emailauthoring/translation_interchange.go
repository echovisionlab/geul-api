package emailauthoring

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TemplateTranslationInterchangeTarget is Email Template's raw sparse target
// projection. Locale row presence owns Exists; Document deliberately retains
// absent Block fields instead of resolving source fallback.
type TemplateTranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Subject  *string
	Document *contentv1.LocalizedRichTextDocument
}

// TemplateTranslationInterchangeMutation is one already-authorized XLIFF
// target mutation. The caller owns the outer transaction and Audit append.
type TemplateTranslationInterchangeMutation struct {
	TargetLocale             string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedPresence         bool
	Subject                  *string
	LocaleMutations          []*contentv1.RichTextBlockLocaleMutation
	ContributorMemberID      string
	Now                      time.Time
}

// TemplateTranslationInterchangeMutationResult reports Email Template's
// aggregate CAS token after an XLIFF target mutation.
type TemplateTranslationInterchangeMutationResult struct {
	Revision string
	Changed  bool
}

// LoadTemplateTranslationInterchangeTarget loads one target without applying
// source fallback. The Translation application has already locked the root and
// authorized source-locale editing in the same transaction.
func LoadTemplateTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	templateID string,
	targetLocale string,
) (TemplateTranslationInterchangeTarget, error) {
	if tx == nil || store == nil {
		return TemplateTranslationInterchangeTarget{}, errs.Internal(errors.New("email template translation interchange dependencies are not configured"))
	}
	templateID, err := canonicalEmailAIDocumentID("email_template_id", templateID)
	if err != nil {
		return TemplateTranslationInterchangeTarget{}, err
	}
	targetLocale, err = normalizeEmailTemplateDocumentLocale(targetLocale)
	if err != nil {
		return TemplateTranslationInterchangeTarget{}, err
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, emailTemplateContentEntity, templateID)
	if err != nil {
		return TemplateTranslationInterchangeTarget{}, err
	}
	domain, err := loadCampaignEmailSourceContext(ctx, tx, emailTemplateContentEntity, templateID)
	if err != nil {
		return TemplateTranslationInterchangeTarget{}, err
	}
	if targetLocale == domain.SourceLocale {
		return TemplateTranslationInterchangeTarget{}, errs.InvalidArgument("target_locale", "must differ from the Email Template source locale")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return TemplateTranslationInterchangeTarget{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if snapshot.Document.Profile != emailContentProfile {
		return TemplateTranslationInterchangeTarget{}, errs.FailedPrecondition("Email Template translation interchange requires the Email content profile")
	}
	metadata, exists, err := loadTemplateInterchangeMetadata(ctx, tx, templateID, targetLocale, "SHARE")
	if err != nil {
		return TemplateTranslationInterchangeTarget{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, targetLocale)
	if err != nil {
		return TemplateTranslationInterchangeTarget{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if !exists && len(document.GetLocaleOverlay().GetBlocks()) != 0 {
		return TemplateTranslationInterchangeTarget{}, errs.InternalMsg("Email Template target Blocks exist without owning locale metadata")
	}
	result := TemplateTranslationInterchangeTarget{
		Exists: exists, Subject: cloneEmailAIDocumentString(metadata.Subject), Document: document,
	}
	if exists {
		result.Revision, err = translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: snapshot.Document.Revision.String(),
			LocaleUpdatedAt: metadata.UpdatedAt,
		})
		if err != nil {
			return TemplateTranslationInterchangeTarget{}, errs.Internal(err)
		}
	}
	return result, nil
}

// ApplyTemplateTranslationInterchange applies one sparse target overlay and
// subject under the Email Template lifecycle fence and Content Document CAS.
func ApplyTemplateTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	references CampaignDeliveryReferences,
	templateID string,
	sourceLocale string,
	input TemplateTranslationInterchangeMutation,
) (TemplateTranslationInterchangeMutationResult, error) {
	if tx == nil || store == nil || references == nil {
		return TemplateTranslationInterchangeMutationResult{}, errs.Internal(errors.New("email template translation interchange dependencies are not configured"))
	}
	templateID, err := canonicalEmailAIDocumentID("email_template_id", templateID)
	if err != nil {
		return TemplateTranslationInterchangeMutationResult{}, err
	}
	input.TargetLocale, err = normalizeEmailTemplateDocumentLocale(input.TargetLocale)
	if err != nil {
		return TemplateTranslationInterchangeMutationResult{}, err
	}
	sourceLocale, err = normalizeEmailTemplateDocumentLocale(sourceLocale)
	if err != nil {
		return TemplateTranslationInterchangeMutationResult{}, err
	}
	if input.TargetLocale == sourceLocale {
		return TemplateTranslationInterchangeMutationResult{}, errs.InvalidArgument("target_locale", "must be a non-source Email Template locale")
	}
	expectedRevision, err := uuid.Parse(strings.TrimSpace(input.ExpectedDocumentRevision))
	if err != nil || expectedRevision == uuid.Nil || expectedRevision.String() != strings.TrimSpace(input.ExpectedDocumentRevision) {
		return TemplateTranslationInterchangeMutationResult{}, errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	contributor, err := uuid.Parse(strings.TrimSpace(input.ContributorMemberID))
	if err != nil || contributor == uuid.Nil || contributor.String() != strings.TrimSpace(input.ContributorMemberID) {
		return TemplateTranslationInterchangeMutationResult{}, errs.InvalidArgument("contributor_member_id", "must be a canonical UUID")
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, emailTemplateContentEntity, templateID)
	if err != nil {
		return TemplateTranslationInterchangeMutationResult{}, err
	}
	batch, err := contentblock.BatchFromRichTextProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		ExpectedRevision:        expectedRevision.String(),
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: input.TargetLocale, Mutations: input.LocaleMutations,
		}},
		ContributorMemberIds: []string{contributor.String()},
	})
	if err != nil {
		return TemplateTranslationInterchangeMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	result, err := applyEmailTemplateTargetMutation(
		ctx, tx, store,
		emailTemplateTargetMutationInput{
			TemplateID: templateID, DocumentID: documentID, Locale: input.TargetLocale,
			Batch: batch, ExpectedDocumentRevision: expectedRevision,
			ExpectedLocaleExists:   &input.ExpectedPresence,
			ExpectedTargetRevision: input.ExpectedTargetRevision,
			AllowCreate:            true, AllowLocaleDeletes: true,
			SetSubject: true, Subject: cloneEmailAIDocumentString(input.Subject), Now: input.Now,
			Fence: campaignEmailContentFence(references, emailTemplateContentEntity, templateID),
		},
	)
	if err != nil {
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &targetConflict) {
			return TemplateTranslationInterchangeMutationResult{}, err
		}
		return TemplateTranslationInterchangeMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	return TemplateTranslationInterchangeMutationResult{
		Revision: result.TargetRevision, Changed: result.Result.Changed,
	}, nil
}

type templateInterchangeMetadata struct {
	Subject   *string
	UpdatedAt *time.Time
}

func loadTemplateInterchangeMetadata(
	ctx context.Context,
	tx *gorm.DB,
	templateID string,
	locale string,
	lock string,
) (templateInterchangeMetadata, bool, error) {
	var row struct {
		Subject   sql.NullString `gorm:"column:subject"`
		UpdatedAt time.Time      `gorm:"column:updated_at"`
	}
	query := tx.WithContext(ctx).Table("email_template_translation").
		Select("subject, updated_at").Where("entity_id = ? AND locale = ?", templateID, locale)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return templateInterchangeMetadata{}, false, nil
	}
	if result.Error != nil {
		return templateInterchangeMetadata{}, false, errs.Internal(result.Error)
	}
	metadata := templateInterchangeMetadata{UpdatedAt: &row.UpdatedAt}
	if row.Subject.Valid {
		value := row.Subject.String
		metadata.Subject = &value
	}
	return metadata, true, nil
}
