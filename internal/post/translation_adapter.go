package post

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	postID string,
) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("post translation content Block store is not configured")
	}
	var sourceState struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("post").Select("source_locale").Where("id = ?::uuid", postID).
		Take(&sourceState).Error; err != nil {
		return nil, err
	}
	metadata, err := loadPostLocaleMetadataRow(ctx, db, postID, sourceState.SourceLocale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, db, postID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, sourceState.SourceLocale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, sourceState.SourceLocale)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		Title: derefString(metadata.Title), Summary: metadata.Summary,
		ContentBlockDocument: document, ContentDocumentRevision: snapshot.Document.Revision.String(),
	}, nil
}
func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	return applyTypedTranslationCandidateWithDB(
		ctx, tx, store, job, candidate, metadata, postTranslationCandidateRequireTitle, auditWriter,
	)
}

type postTranslationCandidateValidation uint8

const (
	postTranslationCandidateRequireTitle postTranslationCandidateValidation = iota
	postTranslationCandidateAllowSparseInterchange
)

func applyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	validation postTranslationCandidateValidation,
	auditWriter domainaudit.Appender,
) error {
	if err := validatePostTranslationCandidate(store, job, candidate, validation); err != nil {
		return err
	}
	documentID, err := loadPostContentDocumentID(ctx, tx, job.EntityID)
	if err != nil {
		return err
	}
	if _, ok := candidate.ProviderPatch(); ok {
		domain, fenceErr := postSystemTranslationDocumentFence(job.EntityID)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		if job.TargetLocale == domain.SourceLocale {
			return applyPostProviderSourceCandidate(
				ctx, tx, store, job, candidate, metadata, auditWriter, documentID, domain,
			)
		}
		state, loadErr := loadPostTargetLocaleStateForDocument(
			ctx, tx, store, job.EntityID, documentID, job.TargetLocale, "UPDATE",
		)
		if loadErr != nil {
			return loadErr
		}
		batch, buildErr := translation.BuildProviderTargetRichTextBatch(
			state.Snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, job.TargetLocale, candidate,
		)
		if buildErr != nil {
			return buildErr
		}
		result, applyErr := applyPostTargetLocaleMutation(ctx, tx, store, postTargetLocaleMutationInput{
			PostID: job.EntityID, Locale: job.TargetLocale,
			ExpectedDocumentRevision: state.Snapshot.Document.Revision,
			Batch:                    &batch,
			AllowCreate:              true,
			UseCurrentTargetRevision: true,
			SetTitle:                 candidate.ProviderUnitRequested("entity:title"),
			Title:                    candidate.Title,
			SetSummary:               candidate.ProviderUnitRequested("entity:summary"),
			Summary:                  candidate.Summary,
			Now:                      metadata.Now,
			Fence:                    postSystemTranslationDocumentFence(job.EntityID),
		})
		if applyErr != nil {
			return applyErr
		}
		if !result.Changed {
			return nil
		}
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errors.New("post translation provider delivery requires its requesting Member")
		}
		operation := sharedtelemetry.AuditItemOperationUpdated
		if result.LocaleCreated {
			operation = sharedtelemetry.AuditItemOperationCreated
		}
		return appendPostMemberLocaleContentAudit(
			ctx, tx, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
			job.EntityID, job.TargetLocale, operation,
		)
	}
	expectedRevision, err := parsePostContentUUID(
		"content_document_revision", candidate.ContentDocumentRevision,
	)
	if err != nil {
		return translation.ErrSourceNoLongerCurrent
	}
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		snapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, documentID, job.SourceLocale)
		if loadErr != nil {
			return loadErr
		}
		batch, err = translation.BuildProviderTargetRichTextBatch(
			snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, job.TargetLocale, candidate,
		)
	} else {
		batch, err = contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
			ExpectedRevision:        expectedRevision.String(),
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: job.TargetLocale, Mutations: candidate.RichTextLocaleMutations(),
			}},
		})
	}
	if err != nil {
		return err
	}
	result, err := store.ApplyBatch(
		ctx, tx, batch, postSystemTranslationDocumentFence(job.EntityID),
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	if err != nil {
		return err
	}
	if result.TranslationSourceChanged {
		return errs.InternalMsg("target Post translation changed the source-owned Block view")
	}
	if result.Changed {
		_, err = applyPostTranslatedMetadata(ctx, tx, job, candidate, metadata, validation)
		return err
	}
	_, err = store.AdvanceRevision(
		ctx,
		tx,
		contentblock.AdvanceInput{
			DocumentID:       documentID,
			ExpectedRevision: result.DocumentRevision,
		},
		postSystemTranslationDocumentFence(job.EntityID),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			changed, err := applyPostTranslatedMetadata(ctx, tx, job, candidate, metadata, validation)
			return contentblock.MetadataEffect{Changed: changed}, err
		},
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	return err
}

func applyPostProviderSourceCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) error {
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return normalizePostContentBlockError(err)
	}
	batch, err := translation.BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, domain.SourceLocale, candidate,
	)
	if err != nil {
		return err
	}
	requesterID, err := uuid.Parse(job.RequestedByMemberID)
	if err != nil || requesterID == uuid.Nil || requesterID.String() != job.RequestedByMemberID {
		return errors.New("post translation provider delivery requires its requesting Member")
	}
	batch.ContributorMemberIDs = []uuid.UUID{requesterID}
	mutation := AIDocumentMutation{
		PostID: job.EntityID, Locale: domain.SourceLocale,
		ObservedSourceLocale: domain.SourceLocale, ObservedLocaleExists: true,
		ExpectedRevision: snapshot.Document.Revision, ContributorMemberID: requesterID,
		Batch: batch,
		Metadata: AIDocumentMetadataPatch{
			SetTitle: candidate.ProviderUnitRequested("entity:title"), Title: candidate.Title,
			SetSummary: candidate.ProviderUnitRequested("entity:summary"), Summary: candidate.Summary,
		},
	}
	result, err := store.ApplyBatchWithMetadata(
		ctx, tx, batch, postSystemTranslationDocumentFence(job.EntityID),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return applyPostAIDocumentMetadata(ctx, tx, mutation, domain.SourceLocale, metadata.Now, nil)
		},
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	if err != nil {
		return err
	}
	if !result.Changed {
		return nil
	}
	if !result.TranslationSourceChanged {
		return errs.InternalMsg("provider source Post translation did not advance source state")
	}
	if err := tx.WithContext(ctx).Model(&model.Post{}).
		Where("id = ?", job.EntityID).UpdateColumn("updated_at", metadata.Now).Error; err != nil {
		return err
	}
	return appendPostMemberLocaleContentAudit(
		ctx, tx, auditWriter, job.RequestedByMemberID, job.EntityID, domain.SourceLocale,
		sharedtelemetry.AuditItemOperationUpdated,
	)
}

func validatePostTranslationCandidate(
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	validation postTranslationCandidateValidation,
) error {
	if store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed post translation candidate and content Block store are required")
	}
	switch validation {
	case postTranslationCandidateRequireTitle:
		if candidate.Title == nil {
			return errs.FailedPrecondition("translated Post title is required")
		}
	case postTranslationCandidateAllowSparseInterchange:
		// A nil title is a missing target unit, while a non-nil empty title is
		// an explicit empty locale value. XLIFF interchange preserves both.
	default:
		return errors.New("post translation candidate validation profile is invalid")
	}
	return nil
}

func applyPostTranslatedMetadata(
	ctx context.Context,
	tx *gorm.DB,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	validation postTranslationCandidateValidation,
) (bool, error) {
	if job == nil || candidate == nil {
		return false, errs.FailedPrecondition("translated Post title is required")
	}
	if validation == postTranslationCandidateRequireTitle && candidate.Title == nil {
		return false, errs.FailedPrecondition("translated Post title is required")
	}
	if validation != postTranslationCandidateRequireTitle && validation != postTranslationCandidateAllowSparseInterchange {
		return false, errors.New("post translation candidate validation profile is invalid")
	}
	var current struct {
		Title   *string `gorm:"column:title"`
		Summary *string `gorm:"column:summary"`
	}
	result := tx.WithContext(ctx).Table("post_translation").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("title", "summary").
		Where("entity_id = ? AND locale = ?", job.EntityID, job.TargetLocale).
		Take(&current)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, errs.Internal(result.Error)
	}
	nextTitle := candidate.Title
	nextSummary := candidate.Summary
	if candidate.HasProviderUnitPatch() {
		if !candidate.ProviderUnitRequested("entity:title") {
			nextTitle = current.Title
		}
		if !candidate.ProviderUnitRequested("entity:summary") {
			nextSummary = current.Summary
		}
	}
	changed := errors.Is(result.Error, gorm.ErrRecordNotFound) ||
		!sameNullableString(current.Title, nextTitle) ||
		!sameNullableString(current.Summary, nextSummary)
	update := tx.WithContext(ctx).Exec(
		`INSERT INTO post_translation (
		 entity_id, locale, title, summary, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
		 title = EXCLUDED.title,
		 summary = EXCLUDED.summary,
		 updated_at = EXCLUDED.updated_at`,
		job.EntityID, job.TargetLocale, nextTitle, nextSummary,
		metadata.Now, metadata.Now,
	)
	if update.Error != nil {
		return false, errs.Internal(update.Error)
	}
	return changed, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
