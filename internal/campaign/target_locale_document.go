package campaign

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
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
		if campaignSnapshotContainsLocale(snapshot, locale) {
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

func campaignSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale && len(overlay.Blocks) != 0 {
			return true
		}
	}
	return false
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

type campaignTargetMutationInput struct {
	CampaignID               string
	DocumentID               uuid.UUID
	Locale                   string
	Batch                    contentblock.Batch
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ExpectedLocaleExists     *bool
	AllowCreate              bool
	// SeedSourceOnCreate is reserved for DCDP bootstrap. Provider and
	// interchange replacements keep omitted/new source units absent.
	SeedSourceOnCreate bool
	// AllowLocaleDeletes is reserved for authoritative whole replacement.
	AllowLocaleDeletes bool
	// OverwriteCurrentTargetCAS applies an accepted provider result over the
	// current target while the source/document fence remains authoritative.
	OverwriteCurrentTargetCAS bool
	SetSubject                bool
	Subject                   *string
	ContributorMember         uuid.UUID
	Now                       time.Time
	Fence                     contentblock.DomainFence
}

type campaignTargetMutationResult struct {
	Result         contentblock.Result
	TargetRevision string
	LocaleCreated  bool
}

func applyCampaignTargetMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input campaignTargetMutationInput,
) (campaignTargetMutationResult, error) {
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return campaignTargetMutationResult{}, err
	}
	state, err := loadCampaignExactLocaleState(
		ctx, tx, store, input.CampaignID, input.DocumentID, input.Locale, true,
	)
	if err != nil {
		return campaignTargetMutationResult{}, err
	}
	if input.Locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale {
		return campaignTargetMutationResult{}, errs.FailedPrecondition("Campaign target locale source changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return campaignTargetMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if input.ExpectedLocaleExists != nil && *input.ExpectedLocaleExists != (state.TargetMetadata != nil) {
		return campaignTargetMutationResult{}, errs.FailedPrecondition(
			"Campaign target locale presence changed; reload before saving",
		)
	}
	if err := validateCampaignTargetBatch(
		input.Batch, input.DocumentID, input.ExpectedDocumentRevision, input.Locale, input.AllowLocaleDeletes,
	); err != nil {
		return campaignTargetMutationResult{}, err
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, input.OverwriteCurrentTargetCAS,
	); err != nil {
		return campaignTargetMutationResult{}, err
	}
	if state.TargetMetadata == nil && !input.AllowCreate {
		return campaignTargetMutationResult{}, errs.FailedPrecondition(
			"Campaign target locale must be explicitly created before collaboration",
		)
	}
	batch := contentblock.CloneBatch(input.Batch)
	if state.TargetMetadata == nil && input.SeedSourceOnCreate {
		batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, input.Locale)
		if err != nil {
			return campaignTargetMutationResult{}, err
		}
	}
	now := input.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Microsecond)
	}
	output := campaignTargetMutationResult{LocaleCreated: state.TargetMetadata == nil}
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
		return campaignTargetMutationResult{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	output.Result = result
	output.TargetRevision, err = deriveCampaignTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return campaignTargetMutationResult{}, err
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Table("campaign").Where("id = ?", input.CampaignID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return campaignTargetMutationResult{}, errs.Internal(err)
		}
		snapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, input.DocumentID, state.SourceLocale)
		if loadErr != nil {
			return campaignTargetMutationResult{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, loadErr)
		}
		if projectErr := projectCampaignTargetMaterializedContent(
			ctx, tx, input.CampaignID, snapshot, input.Locale,
		); projectErr != nil {
			return campaignTargetMutationResult{}, projectErr
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
	if err := translation.ValidateExpectedTargetRevision(expectedTargetRevision, state.TargetRevision, true); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), contributors...),
	}
	group := contentblock.LocaleMutationGroup{Locale: locale}
	for _, overlay := range state.Snapshot.LocaleOverlays {
		if overlay.Locale != locale {
			continue
		}
		for _, block := range overlay.Blocks {
			group.Deletes = append(group.Deletes, block.BlockID)
		}
		break
	}
	sort.Slice(group.Deletes, func(i, j int) bool { return group.Deletes[i].String() < group.Deletes[j].String() })
	if len(group.Deletes) != 0 {
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{group}
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
			now = time.Now().UTC()
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
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil)
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

func validateCampaignTargetBatch(
	batch contentblock.Batch,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	locale string,
	allowLocaleDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "Campaign target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.InvalidArgument("batch", "Campaign target locale cannot mutate the shared Block graph")
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Campaign target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.InvalidArgument("batch", "Campaign target mutation must match the authorized locale")
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.InvalidArgument("batch", "Campaign target locale values use explicit empty and cannot be deleted")
		}
	}
	return nil
}
