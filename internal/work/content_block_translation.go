package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// TranslationJob is the part of a translation job a Work delivery reads.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument loads the canonical Work Rich Text
// document used by translation extraction, through store bound to the
// translation domain's database handle.
func LoadTypedTranslationSourceDocument(ctx context.Context, store Store, workID string) (*translation.SourceDocument, error) {
	source, err := LoadRequiredSourceLocaleMetadata(ctx, store, workID)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, store, workID, Unlocked)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, source.Locale)
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

// ApplyTypedTranslationCandidate persists a translated Work locale overlay
// behind the Work-owned document fence, in the translation application's
// transaction, which joined is bound to.
func ApplyTypedTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
) error {
	store := joined.Store
	if store == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Work translation candidate and content Block store are required")
	}
	documentID, err := loadWorkContentDocumentID(ctx, store, job.EntityID, Unlocked)
	if err != nil {
		return err
	}
	expectedRevision, err := parseWorkContentUUID(
		"content_document_revision",
		candidate.ContentDocumentRevision,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	domain, err := workSystemTranslationDocumentFence(job.EntityID)(ctx, store, documentID)
	if err != nil {
		return err
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		if err := validateWorkProviderSourceTarget(
			job.SourceLocale, job.TargetLocale, domain.SourceLocale,
		); err != nil {
			return err
		}
		return applyWorkProviderSourceCandidate(ctx, joined, job, candidate, entry, documentID, domain)
	}
	state, err := loadWorkTargetLocaleState(ctx, store, job.EntityID, documentID, job.TargetLocale, true)
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
	result, _, err := applyWorkTargetLocaleMutation(ctx, store, workTargetLocaleMutationInput{
		WorkID: job.EntityID, DocumentID: documentID, Locale: job.TargetLocale, Batch: batch,
		Patch: workTargetMetadataPatch{
			EnsureLocale:  true,
			UpdateTitle:   !candidate.HasProviderUnitPatch() || candidate.ProviderUnitRequested("entity:title"),
			Title:         cloneOptionalString(entry.Title),
			UpdateSummary: !candidate.HasProviderUnitPatch() || candidate.ProviderUnitRequested("entity:summary"),
			Summary:       cloneOptionalString(entry.Summary),
		},
		Policy: targetlocale.Policy{
			AllowCreate: true, AllowLocaleDeletes: true, RevisionMode: targetlocale.RevisionCurrent,
		},
		Now: entry.Now, Fence: lockedContentFence(documentID, domain),
	})
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
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
	if err := appendWorkTranslationAudit(
		ctx, joined.Audit, strings.TrimSpace(job.RequestedByMemberID),
		job.EntityID, job.TargetLocale, operation,
	); err != nil {
		return err
	}
	return nil
}

func applyWorkProviderSourceCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) error {
	store := joined.Store
	requesterID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errors.New("work translation provider delivery requires its requesting Member")
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
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
	result, err := store.ApplyContentBatchWithMetadata(
		ctx, batch, lockedContentFence(documentID, domain),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			return applyWorkAIDocumentMetadata(
				ctx, store, job.EntityID, domain.SourceLocale, true, metadataPatch, entry.Now,
			)
		},
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	if err != nil {
		return err
	}
	if !result.Changed {
		return nil
	}
	if !result.TranslationSourceChanged {
		return errors.New("provider source Work translation did not advance source state")
	}
	if err := store.TouchWork(ctx, job.EntityID, entry.Now); err != nil {
		return err
	}
	return appendWorkTranslationAudit(
		ctx, joined.Audit, job.RequestedByMemberID, job.EntityID, domain.SourceLocale,
		sharedtelemetry.AuditItemOperationUpdated,
	)
}

func workSystemTranslationDocumentFence(workID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if work.ID != workID {
			return contentblock.DomainContext{}, fmt.Errorf("work content document changed during translation apply")
		}
		return loadWorkContentDomainContext(ctx, store, workID)
	}
}

// validateWorkProviderSourceTarget discards a late translation when its
// request-time target has since become the Work's canonical source locale.
// A job whose target remains a target may still apply after a source switch.
func validateWorkProviderSourceTarget(jobSourceLocale, jobTargetLocale, currentSourceLocale string) error {
	if jobTargetLocale == currentSourceLocale && jobSourceLocale != currentSourceLocale {
		return translation.ErrSourceNoLongerCurrent
	}
	return nil
}
