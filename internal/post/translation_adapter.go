package post

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TranslationJob is the translation job a delivery or interchange applies.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument reads the Post's source-locale title,
// summary and content document for translation, through store bound to the
// translation domain's database handle.
func LoadTypedTranslationSourceDocument(ctx context.Context, store Store, postID string) (*translation.SourceDocument, error) {
	sourceLocale, err := store.SourceLocale(ctx, postID)
	if err != nil {
		return nil, err
	}
	metadata, err := loadPostLocaleMetadataRow(ctx, store, postID, sourceLocale, false)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, store, postID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, sourceLocale)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		Title: derefString(metadata.Title), Summary: metadata.Summary,
		ContentBlockDocument: document, ContentDocumentRevision: snapshot.Document.Revision.String(),
	}, nil
}

// ApplyTypedTranslationCandidate delivers a provider translation in the
// translation domain's transaction, which store is bound to.
func ApplyTypedTranslationCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	return applyTypedTranslationCandidate(
		ctx, store, job, candidate, metadata, postTranslationCandidateRequireTitle, auditWriter,
	)
}

type postTranslationCandidateValidation uint8

const (
	postTranslationCandidateRequireTitle postTranslationCandidateValidation = iota
	postTranslationCandidateAllowSparseInterchange
)

func applyTypedTranslationCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	validation postTranslationCandidateValidation,
	auditWriter domainaudit.Appender,
) error {
	if err := validatePostTranslationCandidate(candidate, validation); err != nil {
		return err
	}
	documentID, err := loadPostContentDocumentID(ctx, store, job.EntityID)
	if err != nil {
		return err
	}
	if _, ok := candidate.ProviderPatch(); ok {
		domain, fenceErr := postSystemTranslationDocumentFence(job.EntityID)(ctx, store, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		if job.TargetLocale == domain.SourceLocale {
			if err := validatePostProviderSourceTarget(
				job.SourceLocale, job.TargetLocale, domain.SourceLocale,
			); err != nil {
				return err
			}
			return applyPostProviderSourceCandidate(
				ctx, store, job, candidate, metadata, auditWriter, documentID, domain,
			)
		}
		return applyPostProviderTargetCandidate(ctx, store, job, candidate, metadata, auditWriter, documentID)
	}
	expectedRevision, err := parsePostContentUUID(
		"content_document_revision", candidate.ContentDocumentRevision,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		snapshot, loadErr := store.LockedContentSnapshot(ctx, documentID, job.SourceLocale)
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
	result, err := store.ApplyContentBatch(ctx, batch, postSystemTranslationDocumentFence(job.EntityID))
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	if err != nil {
		return err
	}
	if result.TranslationSourceChanged {
		return errors.New("target Post translation changed the source-owned Block view")
	}
	if result.Changed {
		_, err = applyPostTranslatedMetadata(ctx, store, job, candidate, metadata, validation)
		return err
	}
	_, err = store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{
			DocumentID:       documentID,
			ExpectedRevision: result.DocumentRevision,
		},
		postSystemTranslationDocumentFence(job.EntityID),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			changed, err := applyPostTranslatedMetadata(ctx, store, job, candidate, metadata, validation)
			return contentblock.MetadataEffect{Changed: changed}, err
		},
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	return err
}

func applyPostProviderSourceCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) error {
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
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
	result, err := store.ApplyContentBatchWithMetadata(
		ctx, batch, postSystemTranslationDocumentFence(job.EntityID),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			return applyPostAIDocumentMetadata(ctx, store, mutation, domain.SourceLocale, metadata.Now, nil)
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
		return errors.New("provider source Post translation did not advance source state")
	}
	if err := store.TouchPost(ctx, job.EntityID, metadata.Now); err != nil {
		return err
	}
	return appendPostMemberLocaleContentAudit(
		ctx, store, auditWriter, job.RequestedByMemberID, job.EntityID, domain.SourceLocale,
		sharedtelemetry.AuditItemOperationUpdated,
	)
}

func validatePostTranslationCandidate(
	candidate *translation.Candidate,
	validation postTranslationCandidateValidation,
) error {
	if candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed post translation candidate and content Block store are required")
	}
	switch validation {
	case postTranslationCandidateRequireTitle:
		if candidate.Title == nil {
			return domainerr.Precondition("translated Post title is required")
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
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	validation postTranslationCandidateValidation,
) (bool, error) {
	if candidate == nil {
		return false, domainerr.Precondition("translated Post title is required")
	}
	if validation == postTranslationCandidateRequireTitle && candidate.Title == nil {
		return false, domainerr.Precondition("translated Post title is required")
	}
	if validation != postTranslationCandidateRequireTitle && validation != postTranslationCandidateAllowSparseInterchange {
		return false, errors.New("post translation candidate validation profile is invalid")
	}
	current, exists, err := store.LocaleText(ctx, job.EntityID, job.TargetLocale, true)
	if err != nil {
		return false, err
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
	changed := !exists ||
		!nullableStringEqual(current.Title, nextTitle) ||
		!nullableStringEqual(current.Summary, nextSummary)
	if err := store.UpsertLocaleText(ctx, job.EntityID, job.TargetLocale, nextTitle, nextSummary, metadata.Now); err != nil {
		return false, err
	}
	return changed, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// validatePostProviderSourceTarget discards a late translation when its
// request-time target has since become the Post's canonical source locale.
// A job whose target remains a target may still apply after a source switch.
func validatePostProviderSourceTarget(jobSourceLocale, jobTargetLocale, currentSourceLocale string) error {
	if jobTargetLocale == currentSourceLocale && jobSourceLocale != currentSourceLocale {
		return translation.ErrSourceNoLongerCurrent
	}
	return nil
}

// applyPostProviderTargetCandidate writes a provider delivery to a target
// locale and audits it for the requesting Member when it changed.
func applyPostProviderTargetCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
	documentID uuid.UUID,
) error {
	state, loadErr := loadPostTargetLocaleStateForDocument(ctx, store, job.EntityID, documentID, job.TargetLocale, true)
	if loadErr != nil {
		return loadErr
	}
	batch, buildErr := translation.BuildProviderTargetRichTextBatch(
		state.Snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, job.TargetLocale, candidate,
	)
	if buildErr != nil {
		return buildErr
	}
	result, applyErr := applyPostTargetLocaleMutation(ctx, store, postTargetLocaleMutationInput{
		PostID: job.EntityID, Locale: job.TargetLocale,
		ExpectedDocumentRevision: state.Snapshot.Document.Revision,
		Batch:                    &batch,
		Policy:                   targetlocale.Policy{AllowCreate: true, RevisionMode: targetlocale.RevisionCurrent},
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
		ctx, store, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
		job.EntityID, job.TargetLocale, operation,
	)
}
