package legal

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// LoadBlockDocument returns the version's document for collaboration.
func (s *InternalService) LoadBlockDocument(ctx context.Context, kind PolicyKind, query LoadBlockDocumentQuery) (*BlockDocument, error) {
	if _, err := kind.descriptor(); err != nil {
		return nil, err
	}
	entity := string(kind)
	locale, err := canonicalLegalLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	var state legalTargetLocaleState
	err = readLocked(ctx, s.mutations, mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		return authorizeLegalBlockBootstrap(ctx, tx, s.spiceDB, entity, query.ID, query.Principal)
	}), func(ctx context.Context, tx *gorm.DB) error {
		var err error
		state, err = loadLegalTargetLocaleState(ctx, tx, s.contentBlocks, entity, query.ID, locale, false)
		return err
	})
	if err != nil {
		return nil, normalizeLegalContentBlockError(entity, err)
	}
	return state.blockDocument(locale), nil
}

func (state legalTargetLocaleState) blockDocument(locale string) *BlockDocument {
	result := &BlockDocument{
		Document: state.LocalizedDocument, Snapshot: state.Snapshot,
		SourceLocale: state.SourceLocale,
		SourceMetadata: LocaleMetadata{
			Locale: state.SourceMetadata.Locale, Title: state.SourceMetadata.Title,
		},
		Locale: locale,
	}
	if locale == state.SourceLocale {
		result.LocaleExists = true
		metadata := result.SourceMetadata
		result.LocaleMetadata = &metadata
		return result
	}
	if state.TargetMetadata != nil {
		result.LocaleExists = true
		result.LocaleMetadata = &LocaleMetadata{
			Locale: state.TargetMetadata.Locale, Title: state.TargetMetadata.Title,
		}
		targetRevision := state.TargetRevision
		result.TargetRevision = &targetRevision
	}
	return result
}

func legalCollaborationResourceType(kind string) intrav1.CollaborationResourceType {
	if kind == "privacy" {
		return intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY
	}
	return intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

// collaborationSave is one collaboration write: what it changed and the
// target locale content record of a changed target.
type collaborationSave struct {
	change       *DocumentChange
	localeChange *legalLocaleContentChange
}

func (save collaborationSave) audit() mutation.Audit {
	if save.localeChange == nil {
		return mutation.Unchanged()
	}
	return save.localeChange.audit()
}

// saveCollaboration runs one collaboration write on the locked version.
// editor-collab calls as itself: the contributor fence inside the write
// authorizes the room's contributors.
func (s *InternalService) saveCollaboration(
	ctx context.Context,
	entity string,
	entityID string,
	write func(context.Context, *gorm.DB, legalContentDocumentRoot) (collaborationSave, error),
) (*DocumentChange, error) {
	var root legalContentDocumentRoot
	saved, err := mutation.Run(ctx, s.mutations, mutation.Spec[collaborationSave]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			var err error
			root, err = loadLegalContentDocumentRoot(ctx, tx, entity, entityID, true)
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (collaborationSave, error) {
			return write(ctx, tx, root)
		},
		Audit: func(saved collaborationSave) mutation.Audit { return saved.audit() },
	})
	if err != nil {
		return nil, normalizeLegalCollaborationContentBlockError(entity, err)
	}
	return saved.change, nil
}

// ApplyBlockBatch applies the batch and returns what changed.
func (s *InternalService) ApplyBlockBatch(ctx context.Context, kind PolicyKind, command ApplyBlockBatchCommand) (*DocumentChange, error) {
	if _, err := kind.descriptor(); err != nil {
		return nil, err
	}
	entity := string(kind)
	if command.Batch == nil {
		return nil, domainerr.Required("batch")
	}
	locale, err := canonicalLegalLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadLegalContentDocumentID(ctx, s.db, entity, command.ID)
	if err != nil {
		return nil, err
	}
	batch, err := legalCollaborationBatch(entity, documentID, locale, command)
	if err != nil {
		return nil, err
	}
	fence := legalCollaborationDocumentFence(s.checkpoints, entity, command.ID, command.Batch.GetContributorMemberIds(), &batch)
	now := dbtime.Now()
	return s.saveCollaboration(ctx, entity, command.ID, func(ctx context.Context, tx *gorm.DB, root legalContentDocumentRoot) (collaborationSave, error) {
		if locale == root.SourceLocale {
			return s.applySourceBatch(ctx, tx, root, entity, command, batch, fence)
		}
		expectedRevision, err := parseLegalContentUUID("batch.expected_revision", command.Batch.GetExpectedRevision())
		if err != nil {
			return collaborationSave{}, err
		}
		return s.saveTargetLocale(ctx, tx, root, legalTargetLocaleMutationInput{
			EntityType: entity, EntityID: command.ID, Locale: locale,
			ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: command.ExpectedTargetRevision,
			Batch: batch, Now: now, Fence: fence,
		}, true, entity+"_target_block_document_saved")
	})
}

// legalCollaborationBatch decodes a collaboration batch for documentID,
// restoring the locale values the room's affected leaves keep.
func legalCollaborationBatch(entity string, documentID uuid.UUID, locale string, command ApplyBlockBatchCommand) (contentblock.Batch, error) {
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		command.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return contentblock.Batch{}, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		command.Batch.Profile, locale, &storage, command.AffectedLocaleValues,
	); err != nil {
		return contentblock.Batch{}, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	batch, err := contentblock.BatchFromRichTextStorage(documentID, command.Batch.Profile, storage)
	if err != nil {
		return contentblock.Batch{}, normalizeLegalContentBlockError(entity, err)
	}
	return batch, nil
}

// applySourceBatch applies a source room's batch; every locale group must
// be the current source locale.
func (s *InternalService) applySourceBatch(
	ctx context.Context,
	tx *gorm.DB,
	root legalContentDocumentRoot,
	entity string,
	command ApplyBlockBatchCommand,
	batch contentblock.Batch,
	fence contentblock.DomainFence,
) (collaborationSave, error) {
	if command.ExpectedTargetRevision != nil {
		return collaborationSave{}, domainerr.InvalidField("expected_target_revision", "source legal room cannot carry a target revision")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != root.SourceLocale {
			return collaborationSave{}, domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"source legal room mutation must match the current source locale",
			)
		}
	}
	result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, fence)
	if err != nil {
		return collaborationSave{}, err
	}
	if result.Changed {
		if err := s.legalOG.RequestSaved(ctx, tx, entity, command.ID, "", false, entity+"_block_document_saved"); err != nil {
			return collaborationSave{}, err
		}
	}
	return collaborationSave{change: &DocumentChange{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged: result.TranslationSourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: root.SourceLocale,
	}}, nil
}

// saveTargetLocale writes a target room's locale. A changed locale records
// its content change as the request, rebuilds the derived content when the
// Blocks may have changed, and requests the locale's OG.
func (s *InternalService) saveTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	root legalContentDocumentRoot,
	input legalTargetLocaleMutationInput,
	refreshDerived bool,
	reason string,
) (collaborationSave, error) {
	target, err := applyLegalTargetLocaleMutation(ctx, tx, s.contentBlocks, input)
	if err != nil {
		return collaborationSave{}, err
	}
	save := collaborationSave{change: &DocumentChange{
		DocumentRevision: target.DocumentRevision, Changed: target.Changed,
		ChangedLocales: []string{input.Locale}, Locale: input.Locale, TargetRevision: &target.TargetRevision,
	}}
	if !target.Changed {
		return save, nil
	}
	save.localeChange = &legalLocaleContentChange{
		kind: input.EntityType, policyID: input.EntityID, version: root.Version, locale: input.Locale,
		operation: sharedtelemetry.AuditItemOperationUpdated,
	}
	if refreshDerived {
		snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, *root.ContentDocumentID, root.SourceLocale)
		if err != nil {
			return collaborationSave{}, err
		}
		if err := refreshDerivedContent(ctx, tx, input.EntityType, input.EntityID, snapshot, root.SourceLocale, input.Now); err != nil {
			return collaborationSave{}, err
		}
	}
	return save, s.legalOG.RequestSaved(ctx, tx, input.EntityType, input.EntityID, input.Locale, false, reason)
}

// UpdateLocaleMetadata changes a version's title in one locale.
func (s *InternalService) UpdateLocaleMetadata(ctx context.Context, kind PolicyKind, command UpdateLocaleMetadataCommand) (*DocumentChange, error) {
	if _, err := kind.descriptor(); err != nil {
		return nil, err
	}
	entity := string(kind)
	documentID, err := loadLegalContentDocumentID(ctx, s.db, entity, command.EntityID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parseLegalContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	locale, err := canonicalLegalLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	fence := legalCollaborationDocumentFence(s.checkpoints, entity, command.EntityID, command.Contributors, nil)
	now := dbtime.Now()
	return s.saveCollaboration(ctx, entity, command.EntityID, func(ctx context.Context, tx *gorm.DB, root legalContentDocumentRoot) (collaborationSave, error) {
		if locale == root.SourceLocale {
			return s.saveSourceTitle(ctx, tx, root, entity, command, documentID, expectedRevision, fence, now)
		}
		contributors, err := parseLegalContributors(command.Contributors)
		if err != nil {
			return collaborationSave{}, err
		}
		return s.saveTargetLocale(ctx, tx, root, legalTargetLocaleMutationInput{
			EntityType: entity, EntityID: command.EntityID, Locale: locale,
			ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: command.ExpectedTargetRevision,
			Batch: contentblock.Batch{
				DocumentID: documentID, ExpectedRevision: expectedRevision, ContributorMemberIDs: contributors,
			},
			SetTitle: true, Title: command.Title, Now: now, Fence: fence,
		}, false, entity+"_locale_title_saved")
	})
}

func parseLegalContributors(values []string) ([]uuid.UUID, error) {
	contributors := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		parsed, err := parseLegalContentUUID("contributor_member_ids", value)
		if err != nil {
			return nil, err
		}
		contributors = append(contributors, parsed)
	}
	return contributors, nil
}

// saveSourceTitle changes the source title as the next document revision.
func (s *InternalService) saveSourceTitle(
	ctx context.Context,
	tx *gorm.DB,
	root legalContentDocumentRoot,
	entity string,
	command UpdateLocaleMetadataCommand,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	fence contentblock.DomainFence,
	now time.Time,
) (collaborationSave, error) {
	if command.ExpectedTargetRevision != nil {
		return collaborationSave{}, domainerr.InvalidField("expected_target_revision", "source legal room cannot carry a target revision")
	}
	advance, err := s.contentBlocks.AdvanceRevision(
		ctx, tx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			changed, err := updateSourceTitle(ctx, tx, root, entity, command.Title, now)
			return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: changed}, err
		},
	)
	if err != nil {
		return collaborationSave{}, err
	}
	if advance.Changed {
		if err := s.legalOG.RequestSaved(ctx, tx, entity, command.EntityID, root.SourceLocale, false, entity+"_locale_title_saved"); err != nil {
			return collaborationSave{}, err
		}
	}
	return collaborationSave{change: &DocumentChange{
		DocumentRevision: advance.DocumentRevision.String(), Changed: advance.Changed,
		SourceChanged:  advance.TranslationSourceChanged,
		ChangedLocales: []string{root.SourceLocale},
		Locale:         root.SourceLocale,
	}}, nil
}

// updateSourceTitle stores title as the source title; an absent title
// changes nothing.
func updateSourceTitle(
	ctx context.Context,
	tx *gorm.DB,
	root legalContentDocumentRoot,
	entity string,
	title *string,
	now time.Time,
) (bool, error) {
	if title == nil {
		return false, nil
	}
	normalized := strings.TrimSpace(*title)
	if normalized == "" {
		return false, domainerr.InvalidField("title", "must not be empty")
	}
	current, err := loadLegalContentDocumentRoot(ctx, tx, entity, root.ID, false)
	if err != nil {
		return false, err
	}
	if current.Title == normalized {
		return false, nil
	}
	if err := tx.WithContext(ctx).Table(entity+"_history").Where("id = ?", root.ID).
		Updates(structured.Fields{"title": normalized, "updated_at": now}).Error; err != nil {
		return false, err
	}
	return true, nil
}
