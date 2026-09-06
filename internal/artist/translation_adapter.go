package artist

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	artistID string,
) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("artist translation content block store is not configured")
	}
	source, err := loadCreativeDocumentAuthority(ctx, db, artistContentEntity, artistID)
	if err != nil {
		return nil, err
	}
	title, err := loadCreativeSourceTitle(ctx, db, artistContentEntity, artistID, source.SourceLocale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadCreativeContentDocumentID(ctx, db, artistContentEntity, artistID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, source.SourceLocale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, source.SourceLocale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	var row struct {
		RealName *string `gorm:"column:real_name"`
	}
	if err := db.WithContext(ctx).Table("artist").Select("real_name").Where("id = ?", artistID).Take(&row).Error; err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		Title: title, ContentDocumentRevision: snapshot.Document.Revision.String(), ContentBlockDocument: document,
		ProtectedTerms: translation.NormalizeProtectedTerms([]string{title, optionalArtistString(row.RealName)}),
	}, nil
}

func BuildTranslationExtractionPlan(
	job *model.TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job == nil || job.EntityType != artistContentEntity {
		return nil, errors.New("typed Artist translation source is required")
	}
	return translation.BuildRichTextExtractionPlan(job, source, translation.RichTextDocumentFields{})
}

func BuildTranslationCandidate(
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	if plan == nil || plan.EntityType != artistContentEntity {
		return nil, errors.New("typed Artist translation source is required")
	}
	return translation.BuildRichTextCandidate(plan, source, results)
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
		ctx, tx, store, job, candidate, metadata, nil, true, auditWriter, true,
	)
}

// ApplyTypedTranslationInterchangeCandidateWithDB applies an interactive
// XLIFF replacement against the exact target revision observed by the caller.
func ApplyTypedTranslationInterchangeCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	expectedTargetRevision *string,
) error {
	return applyTypedTranslationCandidateWithDB(
		ctx, tx, store, job, candidate, metadata, expectedTargetRevision, false, nil, false,
	)
}

func applyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	expectedTargetRevision *string,
	useCurrentTargetRevision bool,
	auditWriter domainaudit.Appender,
	appendProviderAudit bool,
) error {
	if store == nil || job == nil || job.EntityType != artistContentEntity || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Artist translation candidate and content Block store are required")
	}
	if metadata.Summary != nil || len(metadata.ContentJSON) != 0 || metadata.ContentHTML != nil ||
		metadata.ContentText != nil {
		return errs.FailedPrecondition("Artist translation body is stored as typed Content Blocks")
	}
	documentID, err := loadCreativeContentDocumentID(ctx, tx, artistContentEntity, job.EntityID)
	if err != nil {
		return err
	}
	expectedRevision, err := canonicalArtistUUID(candidate.ContentDocumentRevision)
	if err != nil {
		return translation.ErrSourceNoLongerCurrent
	}
	domain, err := internalCreativeContentFence(artistContentEntity, job.EntityID)(ctx, tx, documentID)
	if err != nil {
		return err
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if auditWriter == nil || parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errors.New("artist translation provider delivery requires its Audit writer and requesting Member")
		}
		snapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
		if loadErr != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, loadErr)
		}
		batch, buildErr := translation.BuildProviderTargetRichTextBatch(
			snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, domain.SourceLocale, candidate,
		)
		if buildErr != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, buildErr)
		}
		batch.ContributorMemberIDs = []uuid.UUID{requesterID}
		result, applyErr := store.ApplyBatch(
			ctx, tx, batch, artistLockedTargetFence(documentID, domain),
		)
		if errors.Is(applyErr, contentblock.ErrStaleRevision) {
			return translation.ErrSourceNoLongerCurrent
		}
		if applyErr != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, applyErr)
		}
		if !result.Changed {
			return nil
		}
		if !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source Artist translation did not advance source state")
		}
		return domainaudit.AppendMember(
			ctx, tx, auditWriter, job.RequestedByMemberID, sharedtelemetry.AuditArtistUpdated,
			func(auditMetadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewArtistLocaleContentAuditRecord(
					auditMetadata, job.EntityID, domain.SourceLocale, sharedtelemetry.AuditItemOperationUpdated,
				)
			},
		)
	}
	state, err := loadArtistTargetLocaleState(
		ctx, tx, store, job.EntityID, documentID, job.TargetLocale, true,
	)
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
			return normalizeCreativeContentBlockError(artistContentEntity, err)
		}
	}
	if err != nil {
		return normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	result, targetRevision, err := applyArtistTargetLocaleBatchAuthority(
		ctx, tx, store, job.EntityID, documentID, job.TargetLocale, batch,
		expectedTargetRevision, true, true, useCurrentTargetRevision, metadata.Now,
		artistLockedTargetFence(documentID, domain),
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	if err != nil {
		return normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	changed := result.Changed
	if candidate.Title != nil {
		titleResult, _, titleErr := updateArtistTargetLocaleTitle(
			ctx, tx, store, job.EntityID, documentID, job.TargetLocale,
			result.DocumentRevision, &targetRevision, candidate.Title, metadata.Now,
			artistLockedTargetFence(documentID, domain),
		)
		if titleErr != nil {
			return titleErr
		}
		changed = changed || titleResult.Changed
	}
	if !appendProviderAudit || !changed {
		return nil
	}
	if auditWriter == nil || strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errors.New("artist translation provider delivery requires its Audit writer and requesting Member")
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !targetPreviouslyExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	memberID := strings.TrimSpace(job.RequestedByMemberID)
	return domainaudit.AppendMember(
		ctx, tx, auditWriter, memberID, sharedtelemetry.AuditArtistUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewArtistLocaleContentAuditRecord(
				metadata, job.EntityID, job.TargetLocale, operation,
			)
		},
	)
}

func RequireLockedSourceLocaleEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, artistID string) error {
	if err := lockArtistParticipantRoot(ctx, tx, artistID); err != nil {
		return err
	}
	return requireLockedArtistPermission(ctx, tx, spiceDB, artistID, policyv1.Artist.Edit)
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
