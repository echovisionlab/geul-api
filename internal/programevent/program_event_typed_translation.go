package programevent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// TranslationJob is the part of a translation job a Program Event delivery
// reads.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	TargetLocale        string
	RequestedByMemberID string
}

type programEventTypedTranslationContent struct {
	Metadata programEventTranslationSourceMetadata
	Snapshot contentblock.Snapshot
}

// LoadTranslationSourceDocument reads the event's source document for
// translation in the caller's read-committed transaction, which this read
// locks the event root in.
func LoadTranslationSourceDocument(ctx context.Context, store Store, eventID string) (*translation.SourceDocument, error) {
	if err := lockTranslationRoot(ctx, store, eventID); err != nil {
		return nil, err
	}
	state, err := loadProgramEventSourceLocale(ctx, store, eventID, false)
	if err != nil {
		return nil, err
	}
	content, err := loadProgramEventTypedTranslationContent(ctx, store, eventID, state.SourceLocale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(content.Snapshot, state.SourceLocale)
	if err != nil {
		return nil, normalizeProgramEventContentBlockError(err)
	}
	return &translation.SourceDocument{
		Summary:                 content.Metadata.Summary,
		ProtectedTerms:          translation.NormalizeProtectedTerms([]string{content.Metadata.Title}),
		ContentBlockDocument:    document,
		ContentDocumentRevision: content.Snapshot.Document.Revision.String(),
	}, nil
}

func loadProgramEventTypedTranslationContent(
	ctx context.Context,
	store Store,
	eventID string,
	locale string,
) (programEventTypedTranslationContent, error) {
	metadata, err := loadTranslationSourceMetadata(ctx, store, eventID, locale)
	if err != nil {
		return programEventTypedTranslationContent{}, err
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, store, eventID, NoLock)
	if err != nil {
		return programEventTypedTranslationContent{}, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, locale)
	if err != nil {
		return programEventTypedTranslationContent{}, normalizeProgramEventContentBlockError(err)
	}
	if snapshot.Document.Profile != programEventContentProfile {
		return programEventTypedTranslationContent{}, domainerr.Precondition(
			"Program Event translation source requires the Program Event content profile",
		)
	}
	return programEventTypedTranslationContent{Metadata: metadata, Snapshot: snapshot}, nil
}

// ApplyTypedTranslationCandidate writes a provider translation of job into
// the translation pipeline's transaction.
func ApplyTypedTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
) error {
	return applyProgramEventTypedTranslationCandidate(ctx, joined, job, candidate, entry, nil, targetlocale.RevisionCurrent)
}

// ApplyTranslationInterchangeCandidate closes Program Event's typed Block
// mutation and exact Member locale-content Audit behind the owning domain
// for XLIFF import, in the interchange's transaction.
func ApplyTranslationInterchangeCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	expectedTargetRevision *string,
) error {
	return applyProgramEventTypedTranslationCandidate(ctx, joined, job, candidate, entry, expectedTargetRevision, targetlocale.RevisionObserved)
}

func applyProgramEventTypedTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	expectedTargetRevision *string,
	revisionMode targetlocale.RevisionMode,
) error {
	store := joined.Store
	if store == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Program Event translation candidate and content Block store are required")
	}
	if job.EntityType != EntityType {
		return fmt.Errorf("typed Program Event translation received entity type %q", job.EntityType)
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != job.TargetLocale {
		return domainerr.Precondition("translated Program Event Block overlay locale does not match the target locale")
	}
	if err := lockTranslationRoot(ctx, store, job.EntityID); err != nil {
		return err
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, store, job.EntityID, true)
	if err != nil {
		return err
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, store, job.EntityID, NoLock)
	if err != nil {
		return err
	}
	current, err := store.ContentSnapshot(ctx, documentID, sourceState.SourceLocale)
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
	fence := programEventContentDocumentFence(job.EntityID, func(ctx context.Context, store Store) error {
		return RequireExists(ctx, store, job.EntityID)
	})
	if job.TargetLocale == sourceState.SourceLocale {
		requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errors.Join(errors.New("program Event translation audit requires requester Member"), parseErr)
		}
		batch.ContributorMemberIDs = []uuid.UUID{requesterID}
		result, applyErr := store.ApplyContentBatchWithMetadata(
			ctx, batch, fence,
			func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
				changed, affectsSource, metadataErr := applyProgramEventLocaleMetadataMutation(
					ctx, store, job.EntityID, sourceState.SourceLocale,
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
			return errors.New("provider source Program Event translation did not advance its source state")
		}
		return appendTranslationLocaleContentAudit(
			ctx, joined.Audit, job.RequestedByMemberID, job.EntityID, sourceState.SourceLocale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	}
	output, err := applyProgramEventTargetLocaleMutation(
		ctx, store,
		programEventTargetLocaleMutationInput{
			EventID: job.EntityID, DocumentID: documentID, Locale: job.TargetLocale,
			Batch: batch, ExpectedDocumentRevision: current.Document.Revision,
			ExpectedTargetRevision: expectedTargetRevision,
			Policy:                 targetlocale.Policy{AllowCreate: true, AllowLocaleDeletes: true, RevisionMode: revisionMode},
			SetSummary:             true, Summary: candidate.Summary, Now: entry.Now, Fence: fence,
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
			return errors.New("program Event translation audit requires requester Member")
		}
		if err := appendTranslationLocaleContentAudit(
			ctx,
			joined.Audit,
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

// appendTranslationLocaleContentAudit records a translation written into
// the caller's transaction, through the appender bound to it. The
// translation pipeline owns that transaction, so the record is appended
// here rather than through mutation.Run; it is attributed to the Member who
// requested the translation.
func appendTranslationLocaleContentAudit(
	ctx context.Context,
	writer domainaudit.Appender,
	memberID string,
	eventID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, nil, writer, memberID, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventLocaleContentAuditRecord(metadata, eventID, locale, operation)
	})
}

func programEventTargetLocaleContentOperation(create, remove, previouslyExists bool) sharedtelemetry.AuditItemOperation {
	switch {
	case remove:
		return sharedtelemetry.AuditItemOperationDeleted
	case create || !previouslyExists:
		return sharedtelemetry.AuditItemOperationCreated
	default:
		return sharedtelemetry.AuditItemOperationUpdated
	}
}
