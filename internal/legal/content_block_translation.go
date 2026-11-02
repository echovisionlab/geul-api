package legal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TranslationJob is the Translation job a typed candidate applies.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument loads the canonical policy Rich Text
// document used for translation extraction.
func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	store Store,
	entityType string,
	entityID string,
) (*translation.SourceDocument, error) {
	kind, err := legalKind(entityType)
	if err != nil {
		return nil, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false)
	if err != nil {
		return nil, err
	}
	content, err := loadLegalTypedTranslationContentForLocale(ctx, store, kind, entityID, root.SourceLocale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(content.snapshot, root.SourceLocale)
	if err != nil {
		return nil, normalizeLegalContentBlockError(entityType, err)
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
	store Store,
	kind *policyKind,
	entityID string,
	locale string,
) (legalTypedTranslationContent, error) {
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return legalTypedTranslationContent{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false)
	if err != nil {
		return legalTypedTranslationContent{}, err
	}
	title := root.Title
	if locale != root.SourceLocale {
		targetTitle, err := store.TranslationTitle(ctx, kind.kind, entityID, locale)
		if err != nil {
			if errors.Is(err, ErrTranslationNotFound) {
				return legalTypedTranslationContent{}, domainerr.ResourceNotFound(kind.entity()+" translation", locale)
			}
			return legalTypedTranslationContent{}, err
		}
		if targetTitle == nil || strings.TrimSpace(*targetTitle) == "" {
			return legalTypedTranslationContent{}, domainerr.Precondition(kind.entity() + " target locale title is missing")
		}
		title = strings.TrimSpace(*targetTitle)
	}
	snapshot, err := store.ContentSnapshot(ctx, *root.ContentDocumentID, locale)
	if err != nil {
		return legalTypedTranslationContent{}, normalizeLegalContentBlockError(kind.entity(), err)
	}
	if snapshot.Document.Profile != legalContentDocumentProfile {
		return legalTypedTranslationContent{}, domainerr.Precondition(kind.entity() + " content document must use the policy profile")
	}
	return legalTypedTranslationContent{title: title, snapshot: snapshot}, nil
}

// applyTypedTranslationCandidate persists one translated policy locale
// behind the archived-policy translation fence, in the transaction joined
// binds.
func applyTypedTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
) error {
	if joined.Store == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("legal typed translation candidate and Content Block transaction are required")
	}
	if err := validateLegalTranslationCandidate(job, candidate); err != nil {
		return err
	}
	kind, err := legalKind(job.EntityType)
	if err != nil {
		return err
	}
	store := joined.Store
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, job.EntityID, true)
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
	snapshot, err := store.LockedContentSnapshot(ctx, *root.ContentDocumentID, root.SourceLocale)
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
		store: store, kind: kind, job: job, root: root, batch: batch,
		title: strings.TrimSpace(*candidate.Title), now: time.Now().UTC(), auditWriter: joined.Audit,
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == root.SourceLocale {
		return apply.applySource(ctx)
	}
	return apply.applyTarget(ctx, snapshot.Document.Revision)
}

func validateLegalTranslationCandidate(job TranslationJob, candidate *translation.Candidate) error {
	if job.EntityType != "terms" && job.EntityType != "privacy" {
		return domainerr.InvalidField("entity_type", "legal typed translation requires terms or privacy")
	}
	if _, err := canonicalLegalLocale(job.SourceLocale); err != nil {
		return err
	}
	if _, err := canonicalLegalLocale(job.TargetLocale); err != nil {
		return err
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != job.TargetLocale {
		return domainerr.InvalidField("document.locale_overlay.locale", "must match the translation target locale")
	}
	if candidate.Title == nil || strings.TrimSpace(*candidate.Title) == "" {
		return domainerr.Precondition("legal target locale title is required")
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
	store       Store
	kind        *policyKind
	job         TranslationJob
	root        Root
	batch       contentblock.Batch
	title       string
	now         time.Time
	auditWriter domainaudit.Appender
}

// applySource writes a provider result whose target is the source locale: the
// source Blocks and the policy title change as one source revision.
func (a legalTranslationApply) applySource(ctx context.Context) error {
	job, root := a.job, a.root
	memberID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if parseErr != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errors.New("legal translation audit requires canonical requester Member")
	}
	batch := a.batch
	batch.ContributorMemberIDs = []uuid.UUID{memberID}
	result, err := a.store.ApplyContentBatchWithMetadata(
		ctx, batch, legalTranslationJobApplyDocumentFence(a.kind, job.EntityID),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			changed, err := store.ReplaceTitle(ctx, a.kind.kind, job.EntityID, a.title, a.now)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
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
		return errors.New("provider source legal translation did not advance source state")
	}
	if err := a.refreshDerivedProjections(ctx); err != nil {
		return err
	}
	return a.appendLocaleContentAudit(ctx, memberID.String(), root.SourceLocale, sharedtelemetry.AuditItemOperationUpdated)
}

// applyTarget creates or overwrites the target locale and its title.
func (a legalTranslationApply) applyTarget(ctx context.Context, expectedRevision uuid.UUID) error {
	job := a.job
	title := a.title
	targetResult, err := applyLegalTargetLocaleMutation(
		ctx,
		a.store,
		legalTargetLocaleMutationInput{
			Kind: a.kind, EntityID: job.EntityID, Locale: job.TargetLocale,
			ExpectedDocumentRevision: expectedRevision,
			Batch:                    a.batch,
			Policy: targetlocale.Policy{
				AllowCreate: true, SeedSource: true, AllowLocaleDeletes: true, RevisionMode: targetlocale.RevisionCurrent,
			},
			SetTitle: true, Title: &title, Now: a.now,
			Fence: legalTranslationJobApplyDocumentFence(a.kind, job.EntityID),
		},
	)
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	if err := a.refreshDerivedProjections(ctx); err != nil {
		return err
	}
	if !targetResult.Changed {
		return nil
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errors.New("legal translation audit requires requester Member")
	}
	return a.appendLocaleContentAudit(
		ctx, job.RequestedByMemberID, job.TargetLocale,
		legalTargetLocaleContentOperation(AITranslationUnchanged, !targetResult.LocaleCreated),
	)
}

// refreshDerivedProjections rebuilds the policy's derived content from the
// source snapshot the write left.
func (a legalTranslationApply) refreshDerivedProjections(ctx context.Context) error {
	snapshot, err := a.store.LockedContentSnapshot(ctx, *a.root.ContentDocumentID, a.root.SourceLocale)
	if err != nil {
		return normalizeLegalContentBlockError(a.job.EntityType, err)
	}
	return refreshDerivedContent(ctx, a.store, a.kind, a.job.EntityID, snapshot, a.root.SourceLocale, a.now)
}

// appendLocaleContentAudit records the translated locale as the job's
// requester in the Translation application's transaction.
func (a legalTranslationApply) appendLocaleContentAudit(
	ctx context.Context,
	memberID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	change := legalLocaleContentChange{
		kind: a.job.EntityType, policyID: a.job.EntityID, version: a.root.Version, locale: locale, operation: operation,
	}
	return domainaudit.AppendMember(ctx, nil, a.auditWriter, memberID, sharedtelemetry.AuditLegalPolicyUpdated, change.record)
}
