package artist

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

// TranslationJob is the part of a translation job an Artist delivery reads.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument reads the Artist's source-locale title
// and content document for translation, through store bound to the
// translation domain's database handle.
func LoadTypedTranslationSourceDocument(ctx context.Context, joined Joined, artistID string) (*translation.SourceDocument, error) {
	store := joined.Store

	sourceLocale, err := loadCreativeDocumentAuthority(ctx, store, artistID, Unlocked)
	if err != nil {
		return nil, err
	}
	title, err := loadCreativeSourceTitle(ctx, store, artistID, sourceLocale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadCreativeContentDocumentID(ctx, store, artistID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, sourceLocale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(err)
	}
	artist, err := store.FindArtist(ctx, artistID)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		Title: title, ContentDocumentRevision: snapshot.Document.Revision.String(), ContentBlockDocument: document,
		ProtectedTerms: translation.NormalizeProtectedTerms([]string{title, optionalArtistString(artist.RealName)}),
	}, nil
}

// ApplyTypedTranslationCandidate applies a provider delivery in the
// translation domain's transaction, which store is bound to, replacing the
// current target row.
func ApplyTypedTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
) error {
	store := joined.Store
	auditWriter := joined.Audit

	return applyTypedTranslationCandidate(
		ctx, store, job, candidate, metadata, nil, targetlocale.RevisionCurrent, auditWriter, true,
	)
}

// ApplyTypedTranslationInterchangeCandidate applies an XLIFF interchange
// candidate under the target row CAS, in the translation domain's
// transaction, which store is bound to.
func ApplyTypedTranslationInterchangeCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	expectedTargetRevision *string,
) error {
	store := joined.Store

	return applyTypedTranslationCandidate(
		ctx, store, job, candidate, metadata, expectedTargetRevision, targetlocale.RevisionObserved, nil, false,
	)
}

func applyTypedTranslationCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	expectedTargetRevision *string,
	revisionMode targetlocale.RevisionMode,
	auditWriter domainaudit.Appender,
	appendProviderAudit bool,
) error {
	if store == nil || job.EntityType != artistContentEntity || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Artist translation candidate and content Block store are required")
	}
	if metadata.Summary != nil || len(metadata.ContentJSON) != 0 || metadata.ContentHTML != nil ||
		metadata.ContentText != nil {
		return domainerr.Precondition("Artist translation body is stored as typed Content Blocks")
	}
	documentID, err := loadCreativeContentDocumentID(ctx, store, job.EntityID)
	if err != nil {
		return err
	}
	expectedRevision, err := canonicalArtistUUID(candidate.ContentDocumentRevision)
	if err != nil {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	domain, err := internalCreativeContentFence(job.EntityID)(ctx, store, documentID)
	if err != nil {
		return err
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		return applyProviderSourceArtistTranslation(ctx, store, job, candidate, auditWriter, documentID, domain)
	}
	state, err := loadArtistTargetLocaleState(ctx, store, job.EntityID, documentID, job.TargetLocale, true)
	if err != nil {
		return err
	}
	targetPreviouslyExists := state.TargetMetadata != nil
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision}
	if candidate.HasProviderUnitPatch() {
		batch, err = translation.BuildProviderTargetRichTextBatch(
			state.Snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, job.TargetLocale, candidate,
		)
	} else if mutations := candidate.RichTextLocaleMutations(); len(mutations) != 0 {
		batch, err = contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, ExpectedRevision: expectedRevision.String(),
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{Locale: job.TargetLocale, Mutations: mutations}},
		})
		if err != nil {
			return normalizeCreativeContentBlockError(err)
		}
	}
	if err != nil {
		return normalizeCreativeContentBlockError(err)
	}
	result, targetRevision, err := applyArtistTargetLocaleMutation(ctx, store, artistTargetLocaleMutationInput{
		ArtistID: job.EntityID, DocumentID: documentID, Locale: job.TargetLocale, Batch: batch,
		ExpectedTargetRevision: expectedTargetRevision,
		Policy:                 targetlocale.Policy{AllowCreate: true, AllowLocaleDeletes: true, RevisionMode: revisionMode},
		Now:                    metadata.Now, Fence: lockedContentFence(documentID, domain),
	})
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	if err != nil {
		return normalizeCreativeContentBlockError(err)
	}
	changed := result.Changed
	if candidate.Title != nil {
		titleResult, _, titleErr := updateArtistTargetLocaleTitle(
			ctx, store, job.EntityID, documentID, job.TargetLocale,
			result.DocumentRevision, &targetRevision, candidate.Title, metadata.Now,
			lockedContentFence(documentID, domain),
		)
		if titleErr != nil {
			return titleErr
		}
		changed = changed || titleResult.Changed
	}
	if !appendProviderAudit || !changed {
		return nil
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errors.New("artist translation provider delivery requires its Audit writer and requesting Member")
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !targetPreviouslyExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	memberID := strings.TrimSpace(job.RequestedByMemberID)
	return store.AppendMemberAudit(
		ctx, auditWriter, memberID, sharedtelemetry.AuditArtistUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewArtistLocaleContentAuditRecord(
				metadata, job.EntityID, job.TargetLocale, operation,
			)
		},
	)
}

// applyProviderSourceArtistTranslation applies a provider's unit patch of
// the source locale as its requesting Member.
func applyProviderSourceArtistTranslation(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	auditWriter domainaudit.Appender,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) error {
	requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errors.New("artist translation provider delivery requires its Audit writer and requesting Member")
	}
	snapshot, loadErr := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if loadErr != nil {
		return normalizeCreativeContentBlockError(loadErr)
	}
	batch, buildErr := translation.BuildProviderTargetRichTextBatch(
		snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, domain.SourceLocale, candidate,
	)
	if buildErr != nil {
		return normalizeCreativeContentBlockError(buildErr)
	}
	batch.ContributorMemberIDs = []uuid.UUID{requesterID}
	result, applyErr := store.ApplyContentBatch(ctx, batch, lockedContentFence(documentID, domain))
	if errors.Is(applyErr, contentblock.ErrStaleRevision) {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, applyErr)
	}
	if applyErr != nil {
		return normalizeCreativeContentBlockError(applyErr)
	}
	if !result.Changed {
		return nil
	}
	if !result.TranslationSourceChanged {
		return errors.New("provider source Artist translation did not advance source state")
	}
	return store.AppendMemberAudit(
		ctx, auditWriter, job.RequestedByMemberID, sharedtelemetry.AuditArtistUpdated,
		func(auditMetadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewArtistLocaleContentAuditRecord(
				auditMetadata, job.EntityID, domain.SourceLocale, sharedtelemetry.AuditItemOperationUpdated,
			)
		},
	)
}

// RequireLockedSourceLocaleEdit locks the Artist row, then requires Edit of
// the principal lockActive locks, for the translation domain's source locale
// change in its transaction, which store is bound to.
func RequireLockedSourceLocaleEdit(ctx context.Context, joined Joined, checker entityparticipant.PermissionChecker, lockActive func(*auth.UserInfo) (bool, error), artistID string) error {
	store := joined.Store

	if err := artistEntity.LockRoot(ctx, store, artistID); err != nil {
		return err
	}
	return artistEntity.RequireLockedPermission(ctx, lockActive, checker, artistID, policyv1.Artist.Edit)
}

func canonicalArtistUUID(value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, fmt.Errorf("canonical UUID is required")
	}
	return parsed, nil
}

func optionalArtistString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
