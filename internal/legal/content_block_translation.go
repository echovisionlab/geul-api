package legal

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
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
	root, err := loadLegalContentDocumentRoot(ctx, tx, job.EntityType, job.EntityID, true)
	if err != nil {
		return err
	}
	if _, err := parseLegalContentUUID(
		"content_document_revision", candidate.ContentDocumentRevision,
	); err != nil {
		return translation.ErrSourceNoLongerCurrent
	}
	snapshot, err := store.LoadSnapshotInTransaction(
		ctx, tx, *root.ContentDocumentID, root.SourceLocale,
	)
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		batch, err = translation.BuildProviderTargetRichTextBatch(
			snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY, job.TargetLocale, candidate,
		)
	} else {
		currentTarget, loadErr := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, job.TargetLocale)
		if loadErr != nil {
			return normalizeLegalContentBlockError(job.EntityType, loadErr)
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
		batch, err = contentblock.BatchFromRichTextSystemProto(*root.ContentDocumentID, &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
			ExpectedRevision:        snapshot.Document.Revision.String(),
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: job.TargetLocale, Mutations: replacement.RichTextLocaleMutations(),
			}},
		})
		if err == nil {
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
	}
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	title := strings.TrimSpace(*candidate.Title)
	now := time.Now().UTC()
	if candidate.HasProviderUnitPatch() && job.TargetLocale == root.SourceLocale {
		memberID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if parseErr != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errs.InternalMsg("legal translation audit requires canonical requester Member")
		}
		batch.ContributorMemberIDs = []uuid.UUID{memberID}
		result, applyErr := store.ApplyBatchWithMetadata(
			ctx, tx, batch, legalTranslationJobApplyDocumentFence(job.EntityType, job.EntityID),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				updated := tx.WithContext(ctx).Table(job.EntityType+"_history").
					Where("id = ? AND title IS DISTINCT FROM ?", job.EntityID, title).
					Updates(map[string]any{"title": title, "updated_at": now})
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
		if applyErr != nil {
			return normalizeLegalContentBlockError(job.EntityType, applyErr)
		}
		if !result.Changed {
			return nil
		}
		if !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source legal translation did not advance source state")
		}
		resultSnapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, *root.ContentDocumentID, root.SourceLocale)
		if loadErr != nil {
			return normalizeLegalContentBlockError(job.EntityType, loadErr)
		}
		if err := (internalLegalDocumentService{db: tx, kind: job.EntityType, contentBlocks: store}).
			refreshDerivedContentProjectionsWithDB(ctx, tx, job.EntityID, resultSnapshot, root.SourceLocale, now); err != nil {
			return err
		}
		return appendLegalTargetLocaleContentAudit(
			ctx, tx, auditWriter, memberID.String(), job.EntityType, job.EntityID, root.Version,
			root.SourceLocale, legalTargetLocaleContentOperation(AITranslationUnchanged, true),
		)
	}
	targetResult, err := applyLegalTargetLocaleMutation(
		ctx,
		tx,
		store,
		legalTargetLocaleMutationInput{
			EntityType: job.EntityType, EntityID: job.EntityID, Locale: job.TargetLocale,
			ExpectedDocumentRevision: snapshot.Document.Revision,
			Batch:                    batch, AllowCreate: true, SeedSourceOnCreate: true,
			OverwriteExistingTarget: true, AllowLocaleDeletes: true,
			SetTitle: true, Title: &title, Now: now,
			Fence: legalTranslationJobApplyDocumentFence(job.EntityType, job.EntityID),
		},
	)
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	resultSnapshot, err := store.LoadSnapshotInTransaction(
		ctx, tx, *root.ContentDocumentID, root.SourceLocale,
	)
	if err != nil {
		return normalizeLegalContentBlockError(job.EntityType, err)
	}
	if err := (internalLegalDocumentService{
		db: tx, kind: job.EntityType, contentBlocks: store,
	}).refreshDerivedContentProjectionsWithDB(
		ctx, tx, job.EntityID, resultSnapshot, root.SourceLocale, now,
	); err != nil {
		return err
	}
	if targetResult.Changed {
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errs.InternalMsg("legal translation audit requires requester Member")
		}
		if err := appendLegalTargetLocaleContentAudit(
			ctx,
			tx,
			auditWriter,
			job.RequestedByMemberID,
			job.EntityType,
			job.EntityID,
			root.Version,
			job.TargetLocale,
			legalTargetLocaleContentOperation(AITranslationUnchanged, !targetResult.LocaleCreated),
		); err != nil {
			return err
		}
	}
	return nil
}
