package programevent

import (
	"context"
	"errors"
	"fmt"
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
)

type programEventTypedTranslationContent struct {
	Metadata programEventTranslationSourceMetadata
	Snapshot contentblock.Snapshot
}

func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	eventID string,
) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("program Event translation content Block store is not configured")
	}
	var output *translation.SourceDocument
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTranslationEntityRootWithDB(ctx, tx, EntityType, eventID); err != nil {
			return err
		}
		state, err := loadProgramEventSourceLocale(ctx, tx, eventID, false)
		if err != nil {
			return err
		}
		content, err := loadProgramEventTypedTranslationContentWithDB(
			ctx, tx, store, eventID, state.SourceLocale,
		)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToLocalizedRichTextDocument(
			content.Snapshot, state.SourceLocale,
		)
		if err != nil {
			return normalizeProgramEventContentBlockError(err)
		}
		output = &translation.SourceDocument{
			Summary:                 content.Metadata.Summary,
			ProtectedTerms:          translation.NormalizeProtectedTerms([]string{content.Metadata.Title}),
			ContentBlockDocument:    document,
			ContentDocumentRevision: content.Snapshot.Document.Revision.String(),
		}
		return nil
	})
	return output, err
}

func loadProgramEventTypedTranslationContentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	eventID string,
	locale string,
) (programEventTypedTranslationContent, error) {
	metadata, err := loadTranslationSourceMetadata(ctx, tx, eventID, locale)
	if err != nil {
		return programEventTypedTranslationContent{}, err
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, tx, eventID, false)
	if err != nil {
		return programEventTypedTranslationContent{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, locale)
	if err != nil {
		return programEventTypedTranslationContent{}, normalizeProgramEventContentBlockError(err)
	}
	if snapshot.Document.Profile != programEventContentProfile {
		return programEventTypedTranslationContent{}, errs.FailedPrecondition(
			"Program Event translation source requires the Program Event content profile",
		)
	}
	return programEventTypedTranslationContent{Metadata: metadata, Snapshot: snapshot}, nil
}

func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	return applyProgramEventTypedTranslationCandidateWithDB(
		ctx, tx, store, job, candidate, entry, nil, true, auditWriter,
	)
}

func applyProgramEventTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	expectedTargetRevision *string,
	overwriteCurrentTargetCAS bool,
	auditWriter domainaudit.Appender,
) error {
	if store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Program Event translation candidate and content Block store are required")
	}
	if job.EntityType != EntityType {
		return fmt.Errorf("typed Program Event translation received entity type %q", job.EntityType)
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != job.TargetLocale {
		return errs.FailedPrecondition("translated Program Event Block overlay locale does not match the target locale")
	}
	if err := lockTranslationEntityRootWithDB(ctx, tx, EntityType, job.EntityID); err != nil {
		return err
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, tx, job.EntityID, true)
	if err != nil {
		return err
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, tx, job.EntityID, false)
	if err != nil {
		return err
	}
	current, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, sourceState.SourceLocale)
	if err != nil {
		return normalizeProgramEventContentBlockError(err)
	}
	batch, err := translation.BuildProviderTargetRichTextBatch(
		current,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
		job.TargetLocale,
		candidate,
	)
	if err != nil {
		return normalizeProgramEventContentBlockError(err)
	}
	fence := programEventContentDocumentFence(job.EntityID, func(ctx context.Context, tx *gorm.DB) error {
		return RequireExists(ctx, tx, job.EntityID)
	})
	if job.TargetLocale == sourceState.SourceLocale {
		requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errs.InternalMsg("Program Event translation audit requires requester Member")
		}
		batch.ContributorMemberIDs = []uuid.UUID{requesterID}
		result, applyErr := store.ApplyBatchWithMetadata(
			ctx, tx, batch, fence,
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				changed, affectsSource, _, metadataErr := applyProgramEventLocaleMetadataMutation(
					ctx, tx, job.EntityID, sourceState.SourceLocale,
					nil, false, candidate.Summary, candidate.ProviderUnitRequested("entity:summary"), entry.Now,
				)
				return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: affectsSource}, metadataErr
			},
		)
		if applyErr != nil {
			return normalizeProgramEventContentBlockError(applyErr)
		}
		if !result.Changed {
			return nil
		}
		if !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source Program Event translation did not advance source state")
		}
		return appendProgramEventMemberLocaleContentAudit(
			ctx, tx, auditWriter, job.RequestedByMemberID, job.EntityID, sourceState.SourceLocale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	}
	output, err := applyProgramEventTargetMutation(
		ctx, tx, store,
		programEventTargetMutationInput{
			EventID: job.EntityID, DocumentID: documentID, Locale: job.TargetLocale,
			Batch: batch, ExpectedDocumentRevision: current.Document.Revision,
			ExpectedTargetRevision: expectedTargetRevision,
			AllowCreate:            true, AllowLocaleDeletes: true,
			OverwriteCurrentTargetCAS: overwriteCurrentTargetCAS,
			SetSummary:                true, Summary: candidate.Summary, Now: entry.Now, Fence: fence,
		},
	)
	if err != nil {
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &targetConflict) {
			return err
		}
		return normalizeProgramEventContentBlockError(err)
	}
	targetChanged := output.Result.Changed
	if targetChanged {
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errs.InternalMsg("Program Event translation audit requires requester Member")
		}
		if err := appendProgramEventMemberLocaleContentAudit(
			ctx,
			tx,
			auditWriter,
			job.RequestedByMemberID,
			job.EntityID,
			job.TargetLocale,
			programEventTargetLocaleContentOperation(output.LocaleCreated, false, !output.LocaleCreated),
		); err != nil {
			return err
		}
	}
	return nil
}

// ApplyTranslationInterchangeCandidateWithDB closes Program Event's typed
// Block mutation and exact Member locale-content Audit behind the owning-domain
// service used by XLIFF import.
func (s *ProgramEventService) ApplyTranslationInterchangeCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	expectedTargetRevision *string,
) error {
	if s == nil || s.auditWriter == nil {
		return errors.New("program event translation interchange dependencies are required")
	}
	return applyProgramEventTypedTranslationCandidateWithDB(
		ctx,
		tx,
		store,
		job,
		candidate,
		entry,
		expectedTargetRevision,
		false,
		s.auditWriter,
	)
}
