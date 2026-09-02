package emailauthoring

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

type emailTemplateExactLocaleMetadata struct {
	Locale    string         `gorm:"column:locale"`
	Subject   sql.NullString `gorm:"column:subject"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
}

type emailTemplateExactLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata emailTemplateExactLocaleMetadata
	TargetMetadata *emailTemplateExactLocaleMetadata
	TargetRevision string
}

func normalizeEmailTemplateDocumentLocale(value string) (string, error) {
	locale := localization.NormalizeExactSupportedLocale(value)
	if locale == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *locale, nil
}

func loadEmailTemplateExactLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	templateID string,
	locale string,
	forUpdate bool,
) (emailTemplateExactLocaleMetadata, bool, error) {
	query := db.WithContext(ctx).Table("email_template_translation").
		Select("locale", "subject", "updated_at").
		Where("entity_id = ? AND locale = ?", templateID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row emailTemplateExactLocaleMetadata
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return emailTemplateExactLocaleMetadata{}, false, nil
		}
		return emailTemplateExactLocaleMetadata{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func loadEmailTemplateExactLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	templateID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (emailTemplateExactLocaleState, error) {
	locale, err := normalizeEmailTemplateDocumentLocale(locale)
	if err != nil {
		return emailTemplateExactLocaleState{}, err
	}
	var domain contentblock.DomainContext

	if forUpdate {
		domain, err = lockCampaignEmailTranslationSource(ctx, tx, emailTemplateContentEntity, templateID)
	} else {
		domain, err = loadCampaignEmailSourceContext(ctx, tx, emailTemplateContentEntity, templateID)
	}
	if err != nil {
		return emailTemplateExactLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return emailTemplateExactLocaleState{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if snapshot.Document.Profile != emailContentProfile {
		return emailTemplateExactLocaleState{}, errs.FailedPrecondition("Email Template content document profile changed")
	}
	source, exists, err := loadEmailTemplateExactLocaleMetadata(ctx, tx, templateID, domain.SourceLocale, forUpdate)
	if err != nil {
		return emailTemplateExactLocaleState{}, err
	}
	if !exists || !source.Subject.Valid || strings.TrimSpace(source.Subject.String) == "" {
		return emailTemplateExactLocaleState{}, errs.FailedPrecondition("Email Template source locale subject is not initialized")
	}
	state := emailTemplateExactLocaleState{
		Snapshot: snapshot, SourceLocale: domain.SourceLocale,
		SourceMetadata: source,
	}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &state.SourceMetadata
		return state, nil
	}
	target, exists, err := loadEmailTemplateExactLocaleMetadata(ctx, tx, templateID, locale, forUpdate)
	if err != nil {
		return emailTemplateExactLocaleState{}, err
	}
	if !exists {
		if emailTemplateSnapshotContainsLocale(snapshot, locale) {
			return emailTemplateExactLocaleState{}, errs.FailedPrecondition("Email Template target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveEmailTemplateTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return emailTemplateExactLocaleState{}, err
	}
	return state, nil
}

func emailTemplateSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale && len(overlay.Blocks) != 0 {
			return true
		}
	}
	return false
}

func deriveEmailTemplateTargetRevision(documentRevision string, metadata emailTemplateExactLocaleMetadata) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func emailTemplateSourceMetadataProjection(state emailTemplateExactLocaleState) *intrav1.EmailTemplateLocaleMetadata {
	subject := state.SourceMetadata.Subject.String
	return &intrav1.EmailTemplateLocaleMetadata{Locale: state.SourceLocale, Subject: &subject}
}

func emailTemplateLocaleMetadataProjection(
	state emailTemplateExactLocaleState,
	locale string,
) *intrav1.EmailTemplateLocaleMetadata {
	if state.TargetMetadata == nil {
		return nil
	}
	subject := state.SourceMetadata.Subject.String
	if state.TargetMetadata.Subject.Valid {
		subject = state.TargetMetadata.Subject.String
	}
	return &intrav1.EmailTemplateLocaleMetadata{Locale: locale, Subject: &subject}
}

func materializeEmailTemplateExactLocaleDocument(
	state emailTemplateExactLocaleState,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	return document, nil
}

type emailTemplateTargetMutationInput struct {
	TemplateID               string
	DocumentID               uuid.UUID
	Locale                   string
	Batch                    contentblock.Batch
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ExpectedLocaleExists     *bool
	AllowCreate              bool
	// SeedSourceOnCreate is the DCDP bootstrap behavior. Provider and
	// interchange whole replacements keep omitted/new source units absent.
	SeedSourceOnCreate bool
	// AllowLocaleDeletes is reserved for authoritative whole replacement.
	AllowLocaleDeletes bool
	// OverwriteCurrentTargetCAS applies an accepted provider result over the
	// current target while preserving the source/document fence.
	OverwriteCurrentTargetCAS bool
	SetSubject                bool
	Subject                   *string
	Now                       time.Time
	Fence                     contentblock.DomainFence
}

type emailTemplateTargetMutationResult struct {
	Result         contentblock.Result
	TargetRevision string
	LocaleCreated  bool
}

func applyEmailTemplateTargetMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input emailTemplateTargetMutationInput,
) (emailTemplateTargetMutationResult, error) {
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return emailTemplateTargetMutationResult{}, err
	}
	state, err := loadEmailTemplateExactLocaleState(
		ctx, tx, store, input.TemplateID, input.DocumentID, input.Locale, true,
	)
	if err != nil {
		return emailTemplateTargetMutationResult{}, err
	}
	if input.Locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale {
		return emailTemplateTargetMutationResult{}, errs.FailedPrecondition("Email Template target locale source changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return emailTemplateTargetMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if input.ExpectedLocaleExists != nil && *input.ExpectedLocaleExists != (state.TargetMetadata != nil) {
		return emailTemplateTargetMutationResult{}, errs.FailedPrecondition(
			"Email Template target locale presence changed; reload before saving",
		)
	}
	if err := validateEmailTemplateTargetBatch(
		input.Batch, input.DocumentID, input.ExpectedDocumentRevision, input.Locale, input.AllowLocaleDeletes,
	); err != nil {
		return emailTemplateTargetMutationResult{}, err
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, input.OverwriteCurrentTargetCAS,
	); err != nil {
		return emailTemplateTargetMutationResult{}, err
	}
	if state.TargetMetadata == nil && !input.AllowCreate {
		return emailTemplateTargetMutationResult{}, errs.FailedPrecondition(
			"Email Template target locale must be explicitly created before collaboration",
		)
	}
	batch := contentblock.CloneBatch(input.Batch)
	if state.TargetMetadata == nil && input.SeedSourceOnCreate {
		batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, input.Locale)
		if err != nil {
			return emailTemplateTargetMutationResult{}, err
		}
	}
	now := input.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Microsecond)
	}
	output := emailTemplateTargetMutationResult{LocaleCreated: state.TargetMetadata == nil}
	var final emailTemplateExactLocaleMetadata
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, input.Locale, input.Fence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				subject := state.SourceMetadata.Subject
				if input.SetSubject {
					subject = nullableEmailTemplateTargetSubject(input.Subject)
				}
				created := tx.WithContext(ctx).Exec(
					`INSERT INTO email_template_translation (
					 entity_id, locale, subject, created_at, updated_at
					) VALUES (?, ?, ?, ?, ?)`,
					input.TemplateID, input.Locale, nullableEmailTemplateSubject(subject), now, now,
				)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(created.Error)
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Email Template target locale could not be created")
				}
				final = emailTemplateExactLocaleMetadata{Locale: input.Locale, Subject: subject, UpdatedAt: now}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
			}
			final = *state.TargetMetadata
			metadataChanged := input.SetSubject && !emailTemplateTargetSubjectsEqual(final.Subject, input.Subject)
			if input.SetSubject {
				final.Subject = nullableEmailTemplateTargetSubject(input.Subject)
			}
			if !contentChanged && !metadataChanged {
				return contentblock.MetadataEffect{}, nil
			}
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updated := tx.WithContext(ctx).Table("email_template_translation").
				Where("entity_id = ? AND locale = ?", input.TemplateID, input.Locale).
				Updates(map[string]any{
					"subject": nullableEmailTemplateSubject(final.Subject), "updated_at": final.UpdatedAt,
				})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Email Template target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		},
	)
	if err != nil {
		return emailTemplateTargetMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	output.Result = result
	output.TargetRevision, err = deriveEmailTemplateTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return emailTemplateTargetMutationResult{}, err
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Table("email_template").Where("id = ?", input.TemplateID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return emailTemplateTargetMutationResult{}, errs.Internal(err)
		}
		snapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, input.DocumentID, state.SourceLocale)
		if loadErr != nil {
			return emailTemplateTargetMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, loadErr)
		}
		if projectErr := projectEmailTemplateTargetMaterializedContent(
			ctx, tx, input.TemplateID, snapshot, input.Locale,
		); projectErr != nil {
			return emailTemplateTargetMutationResult{}, projectErr
		}
	}
	return output, nil
}

func deleteEmailTemplateTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	templateID string,
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
	state, err := loadEmailTemplateExactLocaleState(ctx, tx, store, templateID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale || state.TargetMetadata == nil {
		return contentblock.Result{}, errs.FailedPrecondition("Email Template target locale must exist before deletion")
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
				"DELETE FROM email_template_translation WHERE entity_id = ? AND locale = ?", templateID, locale,
			)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Email Template target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if result.Changed {
		if now.IsZero() {
			now = time.Now().UTC()
		}
		if err := tx.WithContext(ctx).Table("email_template").Where("id = ?", templateID).
			UpdateColumn("updated_at", now.UTC()).Error; err != nil {
			return contentblock.Result{}, errs.Internal(err)
		}
	}
	return result, nil
}

func projectEmailTemplateTargetMaterializedContent(
	ctx context.Context,
	tx *gorm.DB,
	templateID string,
	snapshot contentblock.Snapshot,
	locale string,
) error {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	updated := tx.WithContext(ctx).Table("email_template_translation").
		Where("entity_id = ? AND locale = ?", templateID, locale).
		Updates(map[string]any{"content_html": projection.HTML, "content_text": projection.Text})
	if updated.Error != nil {
		return errs.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return errs.FailedPrecondition("Email Template target locale disappeared while projecting")
	}
	return nil
}

func nullableEmailTemplateSubject(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableEmailTemplateTargetSubject(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func emailTemplateTargetSubjectsEqual(current sql.NullString, next *string) bool {
	if !current.Valid || next == nil {
		return !current.Valid && next == nil
	}
	return current.String == *next
}

func validateEmailTemplateTargetBatch(
	batch contentblock.Batch,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	locale string,
	allowLocaleDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "Email Template target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.InvalidArgument("batch", "Email Template target locale cannot mutate the shared Block graph")
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Email Template target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.InvalidArgument("batch", "Email Template target mutation must match the authorized locale")
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.InvalidArgument("batch", "Email Template target locale values use explicit empty and cannot be deleted")
		}
	}
	return nil
}
