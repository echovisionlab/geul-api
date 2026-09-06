package label

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

func LoadTypedTranslationSourceDocument(ctx context.Context, db *gorm.DB, store *contentblock.Store, labelID string) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("label translation content Block store is not configured")
	}
	source, title, documentID, err := loadTypedLabelTranslationSource(ctx, db, labelID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, source.SourceLocale)
	if err != nil {
		return nil, normalizeLabelContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, source.SourceLocale)
	if err != nil {
		return nil, normalizeLabelContentBlockError(err)
	}
	return &translation.SourceDocument{
		Title: title, ContentDocumentRevision: snapshot.Document.Revision.String(), ContentBlockDocument: document,
		ProtectedTerms: translation.NormalizeProtectedTerms([]string{title}),
	}, nil
}

func BuildTranslationExtractionPlan(job *model.TranslationJob, source *translation.SourceDocument) (*translation.ExtractionPlan, error) {
	if job == nil || job.EntityType != labelContentEntity {
		return nil, errors.New("typed Label translation source is required")
	}
	return translation.BuildRichTextExtractionPlan(job, source, translation.RichTextDocumentFields{})
}

func BuildTranslationCandidate(plan *translation.ExtractionPlan, source *translation.SourceDocument, results map[string]translation.UnitResult) (*translation.Candidate, error) {
	if plan == nil || plan.EntityType != labelContentEntity {
		return nil, errors.New("typed Label translation source is required")
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
	return applyTypedTranslationCandidateWithTargetRevision(
		ctx, tx, store, job, candidate, metadata, auditWriter, nil, true,
	)
}

// ApplyTypedTranslationCandidateWithTargetRevision is the exact Label
// interchange seam. Interactive interchange supplies the target row CAS;
// background provider delivery opts into the current-row replacement policy
// through ApplyTypedTranslationCandidateWithDB above.
func ApplyTypedTranslationCandidateWithTargetRevision(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
	expectedTargetRevision *string,
) error {
	return applyTypedTranslationCandidateWithTargetRevision(
		ctx, tx, store, job, candidate, metadata, auditWriter, expectedTargetRevision, false,
	)
}

func applyTypedTranslationCandidateWithTargetRevision(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	auditWriter domainaudit.Appender,
	expectedTargetRevision *string,
	useCurrentTargetRevision bool,
) error {
	if store == nil || job == nil || job.EntityType != labelContentEntity || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Label translation candidate and content Block store are required")
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errors.New("label translation provider delivery requires its requesting Member")
	}
	if metadata.Summary != nil || len(metadata.ContentJSON) != 0 || metadata.ContentHTML != nil || metadata.ContentText != nil {
		return errs.FailedPrecondition("Label translation body is stored as typed Content Blocks")
	}
	documentID, err := loadLabelContentDocumentID(ctx, tx, job.EntityID)
	if err != nil {
		return err
	}
	domain, err := internalLabelContentFence(job.EntityID)(ctx, tx, documentID)
	if err != nil {
		return err
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		if auditWriter == nil {
			return errors.New("label translation provider delivery requires its Audit writer")
		}
		snapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
		if loadErr != nil {
			return normalizeLabelContentBlockError(loadErr)
		}
		batch, buildErr := translation.BuildProviderTargetRichTextBatch(
			snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, domain.SourceLocale, candidate,
		)
		if buildErr != nil {
			return normalizeLabelContentBlockError(buildErr)
		}
		requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errors.New("label translation provider delivery requires its requesting Member")
		}
		batch.ContributorMemberIDs = []uuid.UUID{requesterID}
		result, applyErr := store.ApplyBatch(ctx, tx, batch, lockedLabelContentFence(documentID, domain))
		if errors.Is(applyErr, contentblock.ErrStaleRevision) {
			return translation.ErrSourceNoLongerCurrent
		}
		if applyErr != nil {
			return normalizeLabelContentBlockError(applyErr)
		}
		if !result.Changed {
			return nil
		}
		if !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source Label translation did not advance source state")
		}
		return appendLabelMemberLocaleAudit(
			ctx, tx, auditWriter, job.RequestedByMemberID, job.EntityID, domain.SourceLocale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	}
	expectedRevision, err := canonicalLabelUUID(candidate.ContentDocumentRevision)
	if err != nil {
		return translation.ErrSourceNoLongerCurrent
	}
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		state, loadErr := loadLabelTargetLocaleState(ctx, tx, store, job.EntityID, documentID, job.TargetLocale, true)
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
	result, err := applyLabelTargetLocaleMutation(ctx, tx, store, labelTargetLocaleMutationInput{
		LabelID: job.EntityID, Locale: job.TargetLocale,
		ExpectedDocumentRevision: batch.ExpectedRevision,
		ExpectedTargetRevision:   expectedTargetRevision,
		Batch:                    batch, AllowCreate: true, SeedSource: false,
		AllowLocaleDeletes:       true,
		UseCurrentTargetRevision: useCurrentTargetRevision,
		Now:                      metadata.Now, Fence: lockedLabelContentFence(documentID, domain),
	})
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	if err != nil {
		return normalizeLabelContentBlockError(err)
	}
	if result.Content.TranslationSourceChanged {
		return errs.InternalMsg("target Label translation changed the source-owned Block view")
	}
	if !result.Content.Changed {
		return nil
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if result.Created {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	if err := appendLabelMemberTargetLocaleAudit(
		ctx, tx, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
		job.EntityID, job.TargetLocale, operation,
	); err != nil {
		return err
	}
	return nil
}

func RequireLockedSourceLocaleEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, labelID string) error {
	if err := lockLabelParticipantRoot(ctx, tx, labelID); err != nil {
		return err
	}
	return requireLockedLabelPermission(ctx, tx, spiceDB, labelID, policyv1.Label.Edit)
}

func loadTypedLabelTranslationSource(ctx context.Context, db *gorm.DB, labelID string) (*labelSourceAuthority, string, uuid.UUID, error) {
	source, err := loadLabelSourceAuthority(ctx, db, labelID, "")
	if err != nil {
		return nil, "", uuid.Nil, err
	}
	title, err := loadLabelTranslationTitle(ctx, db, labelID, source.SourceLocale)
	if err != nil {
		return nil, "", uuid.Nil, err
	}
	documentID, err := loadLabelContentDocumentID(ctx, db, labelID)
	if err != nil {
		return nil, "", uuid.Nil, err
	}
	return &source, title, documentID, nil
}

func loadLabelTranslationTitle(ctx context.Context, db *gorm.DB, labelID, locale string) (string, error) {
	var row struct {
		Title string `gorm:"column:title"`
	}
	if err := db.WithContext(ctx).Table("label_translation").Select("title").Where("entity_id = ? AND locale = ?", labelID, locale).Take(&row).Error; err != nil {
		return "", err
	}
	return row.Title, nil
}

func canonicalLabelUUID(value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, fmt.Errorf("canonical UUID is required")
	}
	return parsed, nil
}
