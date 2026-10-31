package legal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// LoadTypedTranslationSourceDocument loads the canonical policy Rich Text
// document used for translation extraction.
func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	kind string,
	entityID string,
) (*translation.SourceDocument, error) {
	root, err := loadLegalContentDocumentRoot(ctx, db, kind, entityID, false)
	if err != nil {
		return nil, err
	}
	content, err := loadLegalTypedTranslationContentForLocale(
		ctx, db, store, kind, entityID, root.SourceLocale,
	)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(content.snapshot, root.SourceLocale)
	if err != nil {
		return nil, normalizeLegalContentBlockError(kind, err)
	}
	return &translation.SourceDocument{
		SourceLocale:            root.SourceLocale,
		Title:                   content.title,
		ContentDocumentRevision: content.snapshot.Document.Revision.String(),
		ContentBlockDocument:    document,
	}, nil
}

type legalTypedTranslationContent struct {
	title    string
	snapshot contentblock.Snapshot
}

func loadLegalTypedTranslationContentForLocale(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	kind string,
	entityID string,
	locale string,
) (legalTypedTranslationContent, error) {
	if store == nil {
		return legalTypedTranslationContent{}, errs.InternalMsg(kind + " translation Content Block store is not configured")
	}
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return legalTypedTranslationContent{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, db, kind, entityID, false)
	if err != nil {
		return legalTypedTranslationContent{}, err
	}
	title := root.Title
	if locale != root.SourceLocale {
		var target struct {
			Title *string `gorm:"column:title"`
		}
		if err := db.WithContext(ctx).Table(kind+"_translation").
			Select("title").
			Where("entity_id = ? AND locale = ?", entityID, locale).
			Take(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return legalTypedTranslationContent{}, errs.NotFound(kind+" translation", locale)
			}
			return legalTypedTranslationContent{}, errs.Internal(err)
		}
		if target.Title == nil || strings.TrimSpace(*target.Title) == "" {
			return legalTypedTranslationContent{}, errs.FailedPrecondition(kind + " target locale title is missing")
		}
		title = strings.TrimSpace(*target.Title)
	}
	snapshot, err := store.LoadSnapshot(ctx, db, *root.ContentDocumentID, locale)
	if err != nil {
		return legalTypedTranslationContent{}, normalizeLegalContentBlockError(kind, err)
	}
	if snapshot.Document.Profile != legalContentDocumentProfile {
		return legalTypedTranslationContent{}, errs.FailedPrecondition(kind + " content document must use the policy profile")
	}
	return legalTypedTranslationContent{title: title, snapshot: snapshot}, nil
}

// ApplyTypedTranslationCandidateWithDB persists one translated policy locale
// behind the archived-policy translation fence.
func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	auditWriter domainaudit.Appender,
) error {
	if tx == nil || store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errs.Internal(errors.New("legal typed translation candidate and Content Block transaction are required"))
	}
	if err := validateLegalTranslationCandidate(job, candidate); err != nil {
		return err
	}
	root, err := loadLegalContentDocumentRoot(ctx, tx, job.EntityType, job.EntityID, true)
	if err != nil {
		return err
	}
	expectedRevision, err := parseLegalContentUUID(
		"content_document_revision", candidate.ContentDocumentRevision,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	// A provider result whose target is now the source locale was requested
	// for a different source, matching validatePostProviderSourceTarget.
	if candidate.HasProviderUnitPatch() && job.TargetLocale == root.SourceLocale &&
		job.SourceLocale != root.SourceLocale {
		return translation.ErrSourceNoLongerCurrent
	}
	snapshot, err := store.LoadSnapshotInTransaction(
		ctx, tx, *root.ContentDocumentID, root.SourceLocale,
	)
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	// A whole-locale candidate translates the source at its revision; Post
	// discards one whose source has since changed the same way.
	if !candidate.HasProviderUnitPatch() && expectedRevision != snapshot.Document.Revision {
		return translation.ErrSourceNoLongerCurrent
	}
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		batch, err = translation.BuildProviderTargetRichTextBatch(
			snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY, job.TargetLocale, candidate,
		)
	} else {
		batch, err = buildLegalWholeLocaleBatch(snapshot, *root.ContentDocumentID, job.TargetLocale, candidate)
	}
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	apply := legalTranslationApply{
		store: store, job: job, root: root, batch: batch,
		title: strings.TrimSpace(*candidate.Title), now: time.Now().UTC(), auditWriter: auditWriter,
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == root.SourceLocale {
		return apply.applySource(ctx, tx)
	}
	return apply.applyTarget(ctx, tx, snapshot.Document.Revision)
}

func validateLegalTranslationCandidate(job *model.TranslationJob, candidate *translation.Candidate) error {
	if job.EntityType != "terms" && job.EntityType != "privacy" {
		return errs.InvalidArgument("entity_type", "legal typed translation requires terms or privacy")
	}
	if _, err := canonicalLegalLocale(job.SourceLocale); err != nil {
		return err
	}
	if _, err := canonicalLegalLocale(job.TargetLocale); err != nil {
		return err
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != job.TargetLocale {
		return errs.InvalidArgument("document.locale_overlay.locale", "must match the translation target locale")
	}
	if candidate.Title == nil || strings.TrimSpace(*candidate.Title) == "" {
		return errs.FailedPrecondition("legal target locale title is required")
	}
	return nil
}

// buildLegalWholeLocaleBatch replaces the target locale with the candidate:
// target Blocks the candidate omits are deleted, and mutations for Blocks the
// source no longer has, or has with another kind, are dropped.
func buildLegalWholeLocaleBatch(
	snapshot contentblock.Snapshot,
	documentID uuid.UUID,
	targetLocale string,
	candidate *translation.Candidate,
) (contentblock.Batch, error) {
	currentTarget, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, targetLocale)
	if err != nil {
		return contentblock.Batch{}, err
	}
	present := make(map[string]struct{}, len(candidate.ContentBlockLocaleOverlay.Blocks))
	for _, block := range candidate.ContentBlockLocaleOverlay.Blocks {
		present[block.GetBlockId()] = struct{}{}
	}
	replacement := *candidate
	deletes := make(map[string]struct{}, len(candidate.ContentBlockLocaleDeletes))
	for _, blockID := range candidate.ContentBlockLocaleDeletes {
		deletes[blockID] = struct{}{}
	}
	for _, block := range currentTarget.GetLocaleOverlay().GetBlocks() {
		if _, retained := present[block.GetBlockId()]; !retained {
			deletes[block.GetBlockId()] = struct{}{}
		}
	}
	replacement.ContentBlockLocaleDeletes = make([]string, 0, len(deletes))
	for blockID := range deletes {
		replacement.ContentBlockLocaleDeletes = append(replacement.ContentBlockLocaleDeletes, blockID)
	}
	sort.Strings(replacement.ContentBlockLocaleDeletes)
	batch, err := contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		ExpectedRevision:        snapshot.Document.Revision.String(),
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: targetLocale, Mutations: replacement.RichTextLocaleMutations(),
		}},
	})
	if err != nil {
		return contentblock.Batch{}, err
	}
	retainCurrentBlockMutations(&batch, snapshot)
	return batch, nil
}

// retainCurrentBlockMutations drops upserts whose Block is gone or changed
// kind and deletes of Blocks that are gone.
func retainCurrentBlockMutations(batch *contentblock.Batch, snapshot contentblock.Snapshot) {
	currentKinds := make(map[uuid.UUID]string, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		currentKinds[block.ID] = block.Kind
	}
	for groupIndex := range batch.LocaleGroups {
		group := &batch.LocaleGroups[groupIndex]
		accepted := group.Upserts[:0]
		for _, upsert := range group.Upserts {
			if currentKinds[upsert.BlockID] == upsert.ExpectedKind {
				accepted = append(accepted, upsert)
			}
		}
		group.Upserts = accepted
		acceptedDeletes := group.Deletes[:0]
		for _, blockID := range group.Deletes {
			if _, exists := currentKinds[blockID]; exists {
				acceptedDeletes = append(acceptedDeletes, blockID)
			}
		}
		group.Deletes = acceptedDeletes
	}
}

// legalTranslationApply writes one validated translation batch.
type legalTranslationApply struct {
	store       *contentblock.Store
	job         *model.TranslationJob
	root        legalContentDocumentRoot
	batch       contentblock.Batch
	title       string
	now         time.Time
	auditWriter domainaudit.Appender
}

// applySource writes a provider result whose target is the source locale: the
// source Blocks and the policy title change as one source revision.
func (a legalTranslationApply) applySource(ctx context.Context, tx *gorm.DB) error {
	job, root := a.job, a.root
	memberID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if parseErr != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errs.InternalMsg("legal translation audit requires canonical requester Member")
	}
	batch := a.batch
	batch.ContributorMemberIDs = []uuid.UUID{memberID}
	result, err := a.store.ApplyBatchWithMetadata(
		ctx, tx, batch, legalTranslationJobApplyDocumentFence(job.EntityType, job.EntityID),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			updated := tx.WithContext(ctx).Table(job.EntityType+"_history").
				Where("id = ? AND title IS DISTINCT FROM ?", job.EntityID, a.title).
				Updates(map[string]any{"title": a.title, "updated_at": a.now})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			changed := updated.RowsAffected != 0
			return contentblock.MetadataEffect{
				Changed: changed, AffectsTranslationSource: changed, SourceLocale: root.SourceLocale,
				ChangedLocales: []string{root.SourceLocale},
			}, nil
		},
	)
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	if !result.Changed {
		return nil
	}
	if !result.TranslationSourceChanged {
		return errs.InternalMsg("provider source legal translation did not advance source state")
	}
	if err := a.refreshDerivedProjections(ctx, tx); err != nil {
		return err
	}
	return appendLegalTargetLocaleContentAudit(
		ctx, tx, a.auditWriter, memberID.String(), job.EntityType, job.EntityID, root.Version,
		root.SourceLocale, legalTargetLocaleContentOperation(AITranslationUnchanged, true),
	)
}

// applyTarget creates or overwrites the target locale and its title.
func (a legalTranslationApply) applyTarget(ctx context.Context, tx *gorm.DB, expectedRevision uuid.UUID) error {
	job := a.job
	title := a.title
	targetResult, err := applyLegalTargetLocaleMutation(
		ctx,
		tx,
		a.store,
		legalTargetLocaleMutationInput{
			EntityType: job.EntityType, EntityID: job.EntityID, Locale: job.TargetLocale,
			ExpectedDocumentRevision: expectedRevision,
			Batch:                    a.batch,
			Policy: targetlocale.Policy{
				AllowCreate: true, SeedSource: true, AllowLocaleDeletes: true, RevisionMode: targetlocale.RevisionCurrent,
			},
			SetTitle: true, Title: &title, Now: a.now,
			Fence: legalTranslationJobApplyDocumentFence(job.EntityType, job.EntityID),
		},
	)
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	if err := a.refreshDerivedProjections(ctx, tx); err != nil {
		return err
	}
	if !targetResult.Changed {
		return nil
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errs.InternalMsg("legal translation audit requires requester Member")
	}
	return appendLegalTargetLocaleContentAudit(
		ctx,
		tx,
		a.auditWriter,
		job.RequestedByMemberID,
		job.EntityType,
		job.EntityID,
		a.root.Version,
		job.TargetLocale,
		legalTargetLocaleContentOperation(AITranslationUnchanged, !targetResult.LocaleCreated),
	)
}

// refreshDerivedProjections rebuilds the policy's derived content from the
// source snapshot the write left.
func (a legalTranslationApply) refreshDerivedProjections(ctx context.Context, tx *gorm.DB) error {
	snapshot, err := a.store.LoadSnapshotInTransaction(ctx, tx, *a.root.ContentDocumentID, a.root.SourceLocale)
	if err != nil {
		return normalizeLegalContentBlockError(a.job.EntityType, err)
	}
	return (internalLegalDocumentService{db: tx, kind: a.job.EntityType, contentBlocks: a.store}).
		refreshDerivedContentProjectionsWithDB(ctx, tx, a.job.EntityID, snapshot, a.root.SourceLocale, a.now)
}
