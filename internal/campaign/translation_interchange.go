package campaign

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

// TranslationInterchangeTarget is Campaign's raw sparse target projection.
// Locale row presence owns Exists; Document deliberately keeps absent Block
// values absent so readers can resolve source fallback without materializing it
// into the editable target.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Subject  *string
	Document *contentv1.LocalizedRichTextDocument
}

// TranslationInterchangeMutation is one already-authorized XLIFF target
// mutation. The Translation application owns the outer transaction and the
// adapter appends the typed Member Audit before commit.
type TranslationInterchangeMutation struct {
	TargetLocale             string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedPresence         bool
	Subject                  *string
	LocaleMutations          []*contentv1.RichTextBlockLocaleMutation
	ContributorMemberID      string
	Now                      time.Time
}

// TranslationInterchangeMutationResult reports Campaign's aggregate CAS token
// after an XLIFF target mutation.
type TranslationInterchangeMutationResult struct {
	Revision string
	Changed  bool
}

// LoadTranslationInterchangeTarget loads one Campaign target without applying
// source fallback. The caller has already locked and authorized the root in the
// same transaction.
func LoadTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	campaignID string,
	targetLocale string,
) (TranslationInterchangeTarget, error) {
	if tx == nil || store == nil {
		return TranslationInterchangeTarget{}, errs.Internal(errors.New("campaign translation interchange dependencies are not configured"))
	}
	campaignID, err := canonicalCampaignAIDocumentID("campaign_id", campaignID)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	targetLocale, err = normalizeCampaignDocumentLocale(targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, campaignContentEntity, campaignID)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	domain, err := loadCampaignEmailSourceContext(ctx, tx, campaignContentEntity, campaignID)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if targetLocale == domain.SourceLocale {
		return TranslationInterchangeTarget{}, errs.InvalidArgument("target_locale", "must differ from the Campaign source locale")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	if snapshot.Document.Profile != emailContentProfile {
		return TranslationInterchangeTarget{}, errs.FailedPrecondition("Campaign translation interchange requires the Email content profile")
	}
	metadata, exists, err := loadCampaignInterchangeMetadata(ctx, tx, campaignID, targetLocale, "SHARE")
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, targetLocale)
	if err != nil {
		return TranslationInterchangeTarget{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	if !exists && len(document.GetLocaleOverlay().GetBlocks()) != 0 {
		return TranslationInterchangeTarget{}, errs.InternalMsg("Campaign target Blocks exist without owning locale metadata")
	}
	result := TranslationInterchangeTarget{
		Exists: exists, Subject: cloneCampaignAIDocumentString(metadata.Subject), Document: document,
	}
	if exists {
		result.Revision, err = translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: snapshot.Document.Revision.String(),
			LocaleUpdatedAt: metadata.UpdatedAt,
		})
		if err != nil {
			return TranslationInterchangeTarget{}, errs.Internal(err)
		}
	}
	return result, nil
}

// ApplyTranslationInterchange applies one sparse Campaign target under draft
// lifecycle enforcement and the Content Document CAS.
func ApplyTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	campaignID string,
	sourceLocale string,
	input TranslationInterchangeMutation,
) (TranslationInterchangeMutationResult, error) {
	if tx == nil || store == nil {
		return TranslationInterchangeMutationResult{}, errs.Internal(errors.New("campaign translation interchange dependencies are not configured"))
	}
	campaignID, err := canonicalCampaignAIDocumentID("campaign_id", campaignID)
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	input.TargetLocale, err = normalizeCampaignDocumentLocale(input.TargetLocale)
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	sourceLocale, err = normalizeCampaignDocumentLocale(sourceLocale)
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	if input.TargetLocale == sourceLocale {
		return TranslationInterchangeMutationResult{}, errs.InvalidArgument("target_locale", "must be a non-source Campaign locale")
	}
	expectedRevision, err := uuid.Parse(strings.TrimSpace(input.ExpectedDocumentRevision))
	if err != nil || expectedRevision == uuid.Nil || expectedRevision.String() != strings.TrimSpace(input.ExpectedDocumentRevision) {
		return TranslationInterchangeMutationResult{}, errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	contributor, err := uuid.Parse(strings.TrimSpace(input.ContributorMemberID))
	if err != nil || contributor == uuid.Nil || contributor.String() != strings.TrimSpace(input.ContributorMemberID) {
		return TranslationInterchangeMutationResult{}, errs.InvalidArgument("contributor_member_id", "must be a canonical UUID")
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, campaignContentEntity, campaignID)
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
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
		return TranslationInterchangeMutationResult{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	result, err := applyCampaignTargetMutation(
		ctx, tx, store,
		campaignTargetMutationInput{
			CampaignID: campaignID, DocumentID: documentID, Locale: input.TargetLocale,
			Batch: batch, ExpectedDocumentRevision: expectedRevision,
			ExpectedLocaleExists:   &input.ExpectedPresence,
			ExpectedTargetRevision: input.ExpectedTargetRevision,
			AllowCreate:            true, AllowLocaleDeletes: true,
			SetSubject: true, Subject: cloneCampaignAIDocumentString(input.Subject), Now: input.Now,
			Fence: campaignEmailContentFence(campaignContentEntity, campaignID),
		},
	)
	if err != nil {
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &targetConflict) {
			return TranslationInterchangeMutationResult{}, err
		}
		return TranslationInterchangeMutationResult{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	return TranslationInterchangeMutationResult{
		Revision: result.TargetRevision, Changed: result.Result.Changed,
	}, nil
}

type campaignInterchangeMetadata struct {
	Subject   *string
	UpdatedAt *time.Time
}

func loadCampaignInterchangeMetadata(
	ctx context.Context,
	tx *gorm.DB,
	campaignID string,
	locale string,
	lock string,
) (campaignInterchangeMetadata, bool, error) {
	var row struct {
		Subject   sql.NullString `gorm:"column:subject"`
		UpdatedAt time.Time      `gorm:"column:updated_at"`
	}
	query := tx.WithContext(ctx).Table("campaign_translation").
		Select("subject, updated_at").Where("entity_id = ? AND locale = ?", campaignID, locale)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return campaignInterchangeMetadata{}, false, nil
	}
	if result.Error != nil {
		return campaignInterchangeMetadata{}, false, errs.Internal(result.Error)
	}
	metadata := campaignInterchangeMetadata{UpdatedAt: &row.UpdatedAt}
	if row.Subject.Valid {
		value := row.Subject.String
		metadata.Subject = &value
	}
	return metadata, true, nil
}
