package emailauthoring

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

// emailTemplateEntityName names the owner in shared content document messages.
const emailTemplateEntityName = "Email Template"

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

func (state emailTemplateExactLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: emailTemplateEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
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
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
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

type emailTemplateTargetLocaleMutationInput struct {
	TemplateID               string
	DocumentID               uuid.UUID
	Locale                   string
	Batch                    contentblock.Batch
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ExpectedLocaleExists     *bool
	Policy                   targetlocale.Policy
	SetSubject               bool
	Subject                  *string
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type emailTemplateTargetLocaleMutationResult struct {
	Result         contentblock.Result
	TargetRevision string
	LocaleCreated  bool
}

func applyEmailTemplateTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input emailTemplateTargetLocaleMutationInput,
) (emailTemplateTargetLocaleMutationResult, error) {
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return emailTemplateTargetLocaleMutationResult{}, err
	}
	state, err := loadEmailTemplateExactLocaleState(
		ctx, tx, store, input.TemplateID, input.DocumentID, input.Locale, true,
	)
	if err != nil {
		return emailTemplateTargetLocaleMutationResult{}, err
	}
	if input.Locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale {
		return emailTemplateTargetLocaleMutationResult{}, errs.FailedPrecondition("Email Template target locale source changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return emailTemplateTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if input.ExpectedLocaleExists != nil && *input.ExpectedLocaleExists != (state.TargetMetadata != nil) {
		return emailTemplateTargetLocaleMutationResult{}, errs.FailedPrecondition(
			"Email Template target locale presence changed; reload before saving",
		)
	}
	batch, err := input.Policy.Admit(state.locked(), input.Locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return emailTemplateTargetLocaleMutationResult{}, err
	}
	now := targetlocale.WriteTime(input.Now)
	output := emailTemplateTargetLocaleMutationResult{LocaleCreated: state.TargetMetadata == nil}
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
		return emailTemplateTargetLocaleMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	output.Result = result
	output.TargetRevision, err = deriveEmailTemplateTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return emailTemplateTargetLocaleMutationResult{}, err
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Table("email_template").Where("id = ?", input.TemplateID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return emailTemplateTargetLocaleMutationResult{}, errs.Internal(err)
		}
		snapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, input.DocumentID, state.SourceLocale)
		if loadErr != nil {
			return emailTemplateTargetLocaleMutationResult{}, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, loadErr)
		}
		if projectErr := projectEmailTemplateTargetMaterializedContent(
			ctx, tx, input.TemplateID, snapshot, input.Locale,
		); projectErr != nil {
			return emailTemplateTargetLocaleMutationResult{}, projectErr
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
			now = dbtime.Now()
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
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
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
