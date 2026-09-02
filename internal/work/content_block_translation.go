package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// LoadTypedTranslationSourceDocument loads the canonical Work Rich Text
// document used by translation extraction.
func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	workID string,
) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("work translation content Block store is not configured")
	}
	source, err := LoadRequiredSourceLocaleMetadata(ctx, db, workID)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, db, workID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, source.Locale)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, source.Locale)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	return &translation.SourceDocument{
		Title:                   optionalString(source.Title),
		Summary:                 cloneOptionalString(source.Summary),
		ContentDocumentRevision: snapshot.Document.Revision.String(),
		ContentBlockDocument:    document,
	}, nil
}

// ApplyTypedTranslationCandidateWithDB persists a translated Work locale
// overlay behind the Work-owned document fence.
func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Work translation candidate and content Block store are required")
	}
	documentID, err := loadWorkContentDocumentID(ctx, tx, job.EntityID)
	if err != nil {
		return err
	}
	expectedRevision, err := parseWorkContentUUID(
		"content_document_revision",
		candidate.ContentDocumentRevision,
	)
	if err != nil {
		return translation.ErrSourceNoLongerCurrent
	}
	domain, err := workSystemTranslationDocumentFence(job.EntityID)(ctx, tx, documentID)
	if err != nil {
		return err
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		return applyWorkProviderSourceCandidate(
			ctx, tx, store, job, candidate, entry, auditWriter, documentID, domain,
		)
	}
	state, err := loadWorkTargetLocaleState(
		ctx, tx, store, job.EntityID, documentID, job.TargetLocale, true,
	)
	if err != nil {
		return err
	}
	targetPreviouslyExists := state.TargetMetadata != nil
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		batch, err = translation.BuildProviderTargetRichTextBatch(
			state.Snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, job.TargetLocale, candidate,
		)
	} else {
		batch, err = contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			ExpectedRevision:        expectedRevision.String(),
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: job.TargetLocale, Mutations: candidate.RichTextLocaleMutations(),
			}},
		})
	}
	if err != nil {
		return err
	}
	result, _, err := applyWorkTargetLocaleBatch(
		ctx, tx, store, job.EntityID, documentID, job.TargetLocale, batch, nil,
		workTargetMetadataPatch{
			EnsureLocale:  true,
			UpdateTitle:   !candidate.HasProviderUnitPatch() || candidate.ProviderUnitRequested("entity:title"),
			Title:         cloneOptionalString(entry.Title),
			UpdateSummary: !candidate.HasProviderUnitPatch() || candidate.ProviderUnitRequested("entity:summary"),
			Summary:       cloneOptionalString(entry.Summary),
		},
		true, false, true, true, entry.Now, workLockedTargetFence(documentID, domain),
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	if err != nil {
		return err
	}
	if result.TranslationSourceChanged {
		return fmt.Errorf("target Work translation changed the source-owned Block view")
	}
	if !result.Changed {
		return nil
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errors.New("work translation provider delivery requires its requesting Member")
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !targetPreviouslyExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	if err := appendWorkMemberTargetLocaleAudit(
		ctx, tx, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
		job.EntityID, job.TargetLocale, operation,
	); err != nil {
		return err
	}
	return nil
}

func applyWorkProviderSourceCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	auditWriter domainaudit.Appender,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) error {
	requesterID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errors.New("work translation provider delivery requires its requesting Member")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return err
	}
	batch, err := translation.BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, domain.SourceLocale, candidate,
	)
	if err != nil {
		return err
	}
	batch.ContributorMemberIDs = []uuid.UUID{requesterID}
	metadataPatch := AIDocumentMetadataPatch{
		SetTitle: candidate.ProviderUnitRequested("entity:title"), Title: cloneOptionalString(entry.Title),
		SetSummary: candidate.ProviderUnitRequested("entity:summary"), Summary: cloneOptionalString(entry.Summary),
	}
	result, err := store.ApplyBatchWithMetadata(
		ctx, tx, batch, workLockedTargetFence(documentID, domain),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return applyWorkAIDocumentMetadata(
				ctx, tx, job.EntityID, domain.SourceLocale, true, metadataPatch, entry.Now,
			)
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
		return errs.InternalMsg("provider source Work translation did not advance source state")
	}
	if err := tx.WithContext(ctx).Model(&model.Work{}).
		Where("id = ?", job.EntityID).UpdateColumn("updated_at", entry.Now).Error; err != nil {
		return err
	}
	return appendWorkMemberTargetLocaleAudit(
		ctx, tx, auditWriter, job.RequestedByMemberID, job.EntityID, domain.SourceLocale,
		sharedtelemetry.AuditItemOperationUpdated,
	)
}

func workSystemTranslationDocumentFence(workID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if work.ID != workID {
			return contentblock.DomainContext{}, fmt.Errorf("work content document changed during translation apply")
		}
		return loadWorkContentDomainContext(ctx, tx, workID)
	}
}
