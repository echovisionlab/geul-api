package label

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// labelContentEntity is the translation entity type of a Label.
const labelContentEntity = "label"

// TranslationJob is the part of a translation job a Label delivery reads.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument reads the Label's source-locale title
// and content document for translation, through store bound to the
// translation domain's database handle.
func LoadTypedTranslationSourceDocument(ctx context.Context, joined Joined, labelID string) (*translation.SourceDocument, error) {
	store := joined.Store

	sourceLocale, title, documentID, err := loadTypedLabelTranslationSource(ctx, store, labelID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return nil, normalizeLabelContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, sourceLocale)
	if err != nil {
		return nil, normalizeLabelContentBlockError(err)
	}
	return &translation.SourceDocument{
		Title: title, ContentDocumentRevision: snapshot.Document.Revision.String(), ContentBlockDocument: document,
	}, nil
}

// ApplyTypedTranslationCandidateWithTargetRevision is the exact Label
// interchange seam. Interactive interchange supplies the target row CAS;
// background provider delivery opts into the current-row replacement policy
// through ApplyTypedTranslationCandidate above.
func ApplyTypedTranslationCandidateWithTargetRevision(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	expectedTargetRevision *string,
) error {
	store := joined.Store
	auditWriter := joined.Audit

	return applyTypedTranslationCandidateWithTargetRevision(
		ctx, store, job, candidate, metadata, auditWriter, expectedTargetRevision, targetlocale.RevisionObserved,
	)
}

func applyTypedTranslationCandidateWithTargetRevision(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
	expectedTargetRevision *string,
	revisionMode targetlocale.RevisionMode,
) error {
	if store == nil || job.EntityType != labelContentEntity || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Label translation candidate and content Block store are required")
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errors.New("label translation provider delivery requires its requesting Member")
	}
	if metadata.Summary != nil || len(metadata.ContentJSON) != 0 || metadata.ContentHTML != nil || metadata.ContentText != nil {
		return domainerr.Precondition("Label translation body is stored as typed Content Blocks")
	}
	documentID, err := loadLabelContentDocumentID(ctx, store, job.EntityID)
	if err != nil {
		return err
	}
	domain, err := internalLabelContentFence(job.EntityID)(ctx, store, documentID)
	if err != nil {
		return err
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		return applyProviderSourceLabelTranslation(ctx, store, job, candidate, auditWriter, documentID, domain)
	}
	expectedRevision, err := canonicalLabelUUID(candidate.ContentDocumentRevision)
	if err != nil {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		state, loadErr := loadLabelTargetLocaleState(ctx, store, job.EntityID, documentID, job.TargetLocale, true)
		if loadErr != nil {
			return loadErr
		}
		batch, err = translation.BuildProviderTargetRichTextBatch(
			state.Snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, job.TargetLocale, candidate,
		)
	} else {
		batch, err = contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
			ExpectedRevision:        expectedRevision.String(),
			LocaleMutationGroups:    []*contentv1.RichTextLocaleMutationGroup{{Locale: job.TargetLocale, Mutations: candidate.RichTextLocaleMutations()}},
		})
	}
	if err != nil {
		return normalizeLabelContentBlockError(err)
	}
	result, err := applyLabelTargetLocaleMutation(ctx, store, labelTargetLocaleMutationInput{
		LabelID: job.EntityID, Locale: job.TargetLocale,
		ExpectedDocumentRevision: batch.ExpectedRevision,
		ExpectedTargetRevision:   expectedTargetRevision,
		Batch:                    batch,
		Policy:                   targetlocale.Policy{AllowCreate: true, AllowLocaleDeletes: true, RevisionMode: revisionMode},
		Now:                      metadata.Now, Fence: lockedContentFence(documentID, domain),
	})
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	if err != nil {
		return normalizeLabelContentBlockError(err)
	}
	if result.Content.TranslationSourceChanged {
		return errors.New("target Label translation changed the source-owned Block view")
	}
	if !result.Content.Changed {
		return nil
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if result.Created {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	if err := appendLabelMemberLocaleAudit(
		ctx, store, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
		job.EntityID, job.TargetLocale, operation,
	); err != nil {
		return err
	}
	return nil
}

// applyProviderSourceLabelTranslation applies a provider's unit patch of
// the source locale as its requesting Member.
func applyProviderSourceLabelTranslation(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	auditWriter domainaudit.Appender,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) error {
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return normalizeLabelContentBlockError(err)
	}
	batch, err := translation.BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, domain.SourceLocale, candidate,
	)
	if err != nil {
		return normalizeLabelContentBlockError(err)
	}
	requesterID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil {
		return fmt.Errorf("label translation provider delivery requires its requesting Member: %w", err)
	}
	if requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errors.New("label translation provider delivery requires its requesting Member")
	}
	batch.ContributorMemberIDs = []uuid.UUID{requesterID}
	result, err := store.ApplyContentBatch(ctx, batch, lockedContentFence(documentID, domain))
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	if err != nil {
		return normalizeLabelContentBlockError(err)
	}
	if !result.Changed {
		return nil
	}
	if !result.TranslationSourceChanged {
		return errors.New("provider source Label translation did not advance source state")
	}
	return appendLabelMemberLocaleAudit(
		ctx, store, auditWriter, job.RequestedByMemberID, job.EntityID, domain.SourceLocale,
		sharedtelemetry.AuditItemOperationUpdated,
	)
}

// RequireLockedSourceLocaleEdit locks the Label row, then requires Edit of
// the principal lockActive locks, for the translation domain's source locale
// change in its transaction, which store is bound to.
func RequireLockedSourceLocaleEdit(ctx context.Context, joined Joined, checker entityparticipant.PermissionChecker, lockActive func(*auth.UserInfo) (bool, error), labelID string) error {
	store := joined.Store

	if err := labelEntity.LockRoot(ctx, store, labelID); err != nil {
		return err
	}
	return labelEntity.RequireLockedPermission(ctx, lockActive, checker, labelID, policyv1.Label.Edit)
}

func loadTypedLabelTranslationSource(ctx context.Context, store Store, labelID string) (string, string, uuid.UUID, error) {
	sourceLocale, err := loadLabelSourceLocale(ctx, store, labelID, Unlocked)
	if err != nil {
		return "", "", uuid.Nil, err
	}
	title, err := store.LocaleTitleText(ctx, labelID, sourceLocale)
	if err != nil {
		return "", "", uuid.Nil, err
	}
	documentID, err := loadLabelContentDocumentID(ctx, store, labelID)
	if err != nil {
		return "", "", uuid.Nil, err
	}
	return sourceLocale, title, documentID, nil
}

func canonicalLabelUUID(value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, fmt.Errorf("canonical UUID is required")
	}
	return parsed, nil
}
