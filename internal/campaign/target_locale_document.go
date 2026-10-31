package campaign

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/targetlocale"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// campaignEntityName names the owner in shared content document messages.
const campaignEntityName = "Campaign"

type campaignExactLocaleMetadata struct {
	Locale    string         `gorm:"column:locale"`
	Subject   sql.NullString `gorm:"column:subject"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
}

type campaignExactLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata campaignExactLocaleMetadata
	TargetMetadata *campaignExactLocaleMetadata
	TargetRevision string
}

func (state campaignExactLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: campaignEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

func normalizeCampaignDocumentLocale(value string) (string, error) {
	locale := localization.NormalizeExactSupportedLocale(value)
	if locale == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *locale, nil
}

func loadCampaignExactLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
	locale string,
	forUpdate bool,
) (campaignExactLocaleMetadata, bool, error) {
	query := db.WithContext(ctx).Table("campaign_translation").
		Select("locale", "subject", "updated_at").
		Where("entity_id = ? AND locale = ?", campaignID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row campaignExactLocaleMetadata
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return campaignExactLocaleMetadata{}, false, nil
		}
		return campaignExactLocaleMetadata{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func loadCampaignExactLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	campaignID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (campaignExactLocaleState, error) {
	locale, err := normalizeCampaignDocumentLocale(locale)
	if err != nil {
		return campaignExactLocaleState{}, err
	}
	domain, err := loadCampaignSourceContext(ctx, tx, campaignID, forUpdate)
	if err != nil {
		return campaignExactLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return campaignExactLocaleState{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	if snapshot.Document.Profile != emailContentProfile {
		return campaignExactLocaleState{}, errs.FailedPrecondition("Campaign content document profile changed")
	}
	source, exists, err := loadCampaignExactLocaleMetadata(ctx, tx, campaignID, domain.SourceLocale, forUpdate)
	if err != nil {
		return campaignExactLocaleState{}, err
	}
	if !exists || !source.Subject.Valid || strings.TrimSpace(source.Subject.String) == "" {
		return campaignExactLocaleState{}, errs.FailedPrecondition("Campaign source locale subject is not initialized")
	}
	state := campaignExactLocaleState{
		Snapshot: snapshot, SourceLocale: domain.SourceLocale,
		SourceMetadata: source,
	}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &state.SourceMetadata
		return state, nil
	}
	target, exists, err := loadCampaignExactLocaleMetadata(ctx, tx, campaignID, locale, forUpdate)
	if err != nil {
		return campaignExactLocaleState{}, err
	}
	if !exists {
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return campaignExactLocaleState{}, errs.FailedPrecondition("Campaign target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveCampaignTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return campaignExactLocaleState{}, err
	}
	return state, nil
}

func deriveCampaignTargetRevision(documentRevision string, metadata campaignExactLocaleMetadata) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func campaignLocaleMetadataProjection(
	state campaignExactLocaleState,
	locale string,
) *intrav1.CampaignLocaleMetadata {
	if state.TargetMetadata == nil {
		return nil
	}
	subject := state.SourceMetadata.Subject.String
	if state.TargetMetadata.Subject.Valid {
		subject = state.TargetMetadata.Subject.String
	}
	return &intrav1.CampaignLocaleMetadata{Locale: locale, Subject: &subject}
}

func campaignSourceMetadataProjection(state campaignExactLocaleState) *intrav1.CampaignLocaleMetadata {
	subject := state.SourceMetadata.Subject.String
	return &intrav1.CampaignLocaleMetadata{Locale: state.SourceLocale, Subject: &subject}
}

func materializeCampaignExactLocaleDocument(
	state campaignExactLocaleState,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	return document, nil
}

type campaignTargetLocaleMutationInput struct {
	CampaignID               string
	DocumentID               uuid.UUID
	Locale                   string
	Batch                    contentblock.Batch
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ExpectedLocaleExists     *bool
	Policy                   targetlocale.Policy
	SetSubject               bool
	Subject                  *string
	ContributorMember        uuid.UUID
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type campaignTargetLocaleMutationResult struct {
	Result         contentblock.Result
	TargetRevision string
	LocaleCreated  bool
}

func applyCampaignTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input campaignTargetLocaleMutationInput,
) (campaignTargetLocaleMutationResult, error) {
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return campaignTargetLocaleMutationResult{}, err
	}
	state, err := loadCampaignExactLocaleState(
		ctx, tx, store, input.CampaignID, input.DocumentID, input.Locale, true,
	)
	if err != nil {
		return campaignTargetLocaleMutationResult{}, err
	}
	if input.Locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale {
		return campaignTargetLocaleMutationResult{}, errs.FailedPrecondition("Campaign target locale source changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return campaignTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if input.ExpectedLocaleExists != nil && *input.ExpectedLocaleExists != (state.TargetMetadata != nil) {
		return campaignTargetLocaleMutationResult{}, errs.FailedPrecondition(
			"Campaign target locale presence changed; reload before saving",
		)
	}
	batch, err := input.Policy.Admit(state.locked(), input.Locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return campaignTargetLocaleMutationResult{}, err
	}
	now := targetlocale.WriteTime(input.Now)
	output := campaignTargetLocaleMutationResult{LocaleCreated: state.TargetMetadata == nil}
	var final campaignExactLocaleMetadata
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, input.Locale, input.Fence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				subject := state.SourceMetadata.Subject
				if input.SetSubject {
					subject = nullableCampaignTargetSubject(input.Subject)
				}
				created := tx.WithContext(ctx).Exec(
					`INSERT INTO campaign_translation (
					 entity_id, locale, subject, created_at, updated_at
					) VALUES (?, ?, ?, ?, ?)`,
					input.CampaignID, input.Locale, nullableCampaignSubject(subject), now, now,
				)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(created.Error)
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Campaign target locale could not be created")
				}
				final = campaignExactLocaleMetadata{Locale: input.Locale, Subject: subject, UpdatedAt: now}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
			}
			final = *state.TargetMetadata
			metadataChanged := input.SetSubject && !campaignTargetSubjectsEqual(final.Subject, input.Subject)
			if input.SetSubject {
				final.Subject = nullableCampaignTargetSubject(input.Subject)
			}
			if !contentChanged && !metadataChanged {
				return contentblock.MetadataEffect{}, nil
			}
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updated := tx.WithContext(ctx).Table("campaign_translation").
				Where("entity_id = ? AND locale = ?", input.CampaignID, input.Locale).
				Updates(map[string]any{
					"subject": nullableCampaignSubject(final.Subject), "updated_at": final.UpdatedAt,
				})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Campaign target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		},
	)
	if err != nil {
		return campaignTargetLocaleMutationResult{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	output.Result = result
	output.TargetRevision, err = deriveCampaignTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return campaignTargetLocaleMutationResult{}, err
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Table("campaign").Where("id = ?", input.CampaignID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return campaignTargetLocaleMutationResult{}, errs.Internal(err)
		}
		snapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, input.DocumentID, state.SourceLocale)
		if loadErr != nil {
			return campaignTargetLocaleMutationResult{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, loadErr)
		}
		if projectErr := projectCampaignTargetMaterializedContent(
			ctx, tx, input.CampaignID, snapshot, input.Locale,
		); projectErr != nil {
			return campaignTargetLocaleMutationResult{}, projectErr
		}
	}
	return output, nil
}

func deleteCampaignTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	campaignID string,
	documentID uuid.UUID,
	locale string,
	expectedDocumentRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadCampaignExactLocaleState(ctx, tx, store, campaignID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale || state.TargetMetadata == nil {
		return contentblock.Result{}, errs.FailedPrecondition("Campaign target locale must exist before deletion")
	}
	if state.Snapshot.Document.Revision != expectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked().CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), contributors...),
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, fence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec(
				"DELETE FROM campaign_translation WHERE entity_id = ? AND locale = ?", campaignID, locale,
			)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Campaign target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	if result.Changed {
		if now.IsZero() {
			now = dbtime.Now()
		}
		if err := tx.WithContext(ctx).Table("campaign").Where("id = ?", campaignID).
			UpdateColumn("updated_at", now.UTC()).Error; err != nil {
			return contentblock.Result{}, errs.Internal(err)
		}
	}
	return result, nil
}

func projectCampaignTargetMaterializedContent(
	ctx context.Context,
	tx *gorm.DB,
	campaignID string,
	snapshot contentblock.Snapshot,
	locale string,
) error {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
	if err != nil {
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	updated := tx.WithContext(ctx).Table("campaign_translation").
		Where("entity_id = ? AND locale = ?", campaignID, locale).
		Updates(map[string]any{"content_html": projection.HTML, "content_text": projection.Text})
	if updated.Error != nil {
		return errs.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return errs.FailedPrecondition("Campaign target locale disappeared while projecting")
	}
	return nil
}

func nullableCampaignSubject(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableCampaignTargetSubject(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func campaignTargetSubjectsEqual(current sql.NullString, next *string) bool {
	if !current.Valid || next == nil {
		return !current.Valid && next == nil
	}
	return current.String == *next
}
