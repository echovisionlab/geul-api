package legal

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// LoadBlockDocument returns the version's document for collaboration.
func (s *InternalService) LoadBlockDocument(ctx context.Context, kind PolicyKind, query LoadBlockDocumentQuery) (*BlockDocument, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return nil, err
	}
	entity := string(kind)
	locale, err := canonicalLegalLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	var state legalTargetLocaleState
	err = readLocked(ctx, s.mutations, mutation.Locked(func(ctx context.Context, store Store, _ mutation.Require) error {
		return authorizeLegalBlockBootstrap(ctx, store, s.spiceDB, descriptor, query.ID, query.SessionID)
	}), func(ctx context.Context, store Store) error {
		var err error
		state, err = loadLegalTargetLocaleState(ctx, store, descriptor, query.ID, locale, false)
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
	kind *policyKind,
	entityID string,
	write func(context.Context, Store, Root) (collaborationSave, error),
) (*DocumentChange, error) {
	var root Root
	saved, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[collaborationSave, Store]{
		Access: mutation.Locked(func(ctx context.Context, store Store, _ mutation.Require) error {
			var err error
			root, err = loadLegalContentDocumentRoot(ctx, store, kind, entityID, true)
			return err
		}),
		Change: func(ctx context.Context, store Store) (collaborationSave, error) {
			return write(ctx, store, root)
		},
		Audit: func(saved collaborationSave) mutation.Audit { return saved.audit() },
	})
	if err != nil {
		return nil, normalizeLegalCollaborationContentBlockError(kind.entity(), err)
	}
	return saved.change, nil
}

// lockedDocumentID reads the version's content document reference under a
// share lock, before the write's own transaction.
func (s *InternalService) lockedDocumentID(ctx context.Context, kind *policyKind, entityID string) (uuid.UUID, error) {
	var documentID uuid.UUID
	err := s.units.ReadCurrent(ctx, func(store Store) error {
		var err error
		documentID, err = loadLegalContentDocumentID(ctx, store, kind, entityID)
		return err
	})
	return documentID, err
}

// ApplyBlockBatch applies the batch and returns what changed.
func (s *InternalService) ApplyBlockBatch(ctx context.Context, kind PolicyKind, command ApplyBlockBatchCommand) (*DocumentChange, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
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
	documentID, err := s.lockedDocumentID(ctx, descriptor, command.ID)
	if err != nil {
		return nil, err
	}
	batch, err := legalCollaborationBatch(entity, documentID, locale, command)
	if err != nil {
		return nil, err
	}
	fence := legalCollaborationDocumentFence(descriptor, command.ID, command.Batch.GetContributorMemberIds(), &batch)
	now := dbtime.Now()
	return s.saveCollaboration(ctx, descriptor, command.ID, func(ctx context.Context, store Store, root Root) (collaborationSave, error) {
		if locale == root.SourceLocale {
			return applySourceBatch(ctx, store, root, descriptor, command, batch, fence)
		}
		expectedRevision, err := parseLegalContentUUID("batch.expected_revision", command.Batch.GetExpectedRevision())
		if err != nil {
			return collaborationSave{}, err
		}
		return saveTargetLocale(ctx, store, root, legalTargetLocaleMutationInput{
			Kind: descriptor, EntityID: command.ID, Locale: locale,
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
func applySourceBatch(
	ctx context.Context,
	store Store,
	root Root,
	kind *policyKind,
	command ApplyBlockBatchCommand,
	batch contentblock.Batch,
	fence ContentFence,
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
	result, err := store.ApplyContentBatch(ctx, batch, fence)
	if err != nil {
		return collaborationSave{}, err
	}
	if result.Changed {
		if err := store.RequestOG(ctx, kind.kind, command.ID, "", false, kind.entity()+"_block_document_saved"); err != nil {
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
func saveTargetLocale(
	ctx context.Context,
	store Store,
	root Root,
	input legalTargetLocaleMutationInput,
	refreshDerived bool,
	reason string,
) (collaborationSave, error) {
	target, err := applyLegalTargetLocaleMutation(ctx, store, input)
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
		kind: input.Kind.entity(), policyID: input.EntityID, version: root.Version, locale: input.Locale,
		operation: sharedtelemetry.AuditItemOperationUpdated,
	}
	if refreshDerived {
		snapshot, err := store.LockedContentSnapshot(ctx, *root.ContentDocumentID, root.SourceLocale)
		if err != nil {
			return collaborationSave{}, err
		}
		if err := refreshDerivedContent(ctx, store, input.Kind, input.EntityID, snapshot, root.SourceLocale, input.Now); err != nil {
			return collaborationSave{}, err
		}
	}
	return save, store.RequestOG(ctx, input.Kind.kind, input.EntityID, input.Locale, false, reason)
}

// UpdateLocaleMetadata changes a version's title in one locale.
func (s *InternalService) UpdateLocaleMetadata(ctx context.Context, kind PolicyKind, command UpdateLocaleMetadataCommand) (*DocumentChange, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return nil, err
	}
	entity := string(kind)
	documentID, err := s.lockedDocumentID(ctx, descriptor, command.EntityID)
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
	fence := legalCollaborationDocumentFence(descriptor, command.EntityID, command.Contributors, nil)
	now := dbtime.Now()
	return s.saveCollaboration(ctx, descriptor, command.EntityID, func(ctx context.Context, store Store, root Root) (collaborationSave, error) {
		if locale == root.SourceLocale {
			return saveSourceTitle(ctx, store, root, descriptor, command, documentID, expectedRevision, fence, now)
		}
		contributors, err := parseLegalContributors(command.Contributors)
		if err != nil {
			return collaborationSave{}, err
		}
		return saveTargetLocale(ctx, store, root, legalTargetLocaleMutationInput{
			Kind: descriptor, EntityID: command.EntityID, Locale: locale,
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
func saveSourceTitle(
	ctx context.Context,
	store Store,
	root Root,
	kind *policyKind,
	command UpdateLocaleMetadataCommand,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	fence ContentFence,
	now time.Time,
) (collaborationSave, error) {
	if command.ExpectedTargetRevision != nil {
		return collaborationSave{}, domainerr.InvalidField("expected_target_revision", "source legal room cannot carry a target revision")
	}
	advance, err := store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		fence,
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			changed, err := updateSourceTitle(ctx, store, root, kind, command.Title, now)
			return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: changed}, err
		},
	)
	if err != nil {
		return collaborationSave{}, err
	}
	if advance.Changed {
		if err := store.RequestOG(ctx, kind.kind, command.EntityID, root.SourceLocale, false, kind.entity()+"_locale_title_saved"); err != nil {
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
	store Store,
	root Root,
	kind *policyKind,
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
	current, err := loadLegalContentDocumentRoot(ctx, store, kind, root.ID, false)
	if err != nil {
		return false, err
	}
	if current.Title == normalized {
		return false, nil
	}
	if err := store.SetTitle(ctx, kind.kind, root.ID, normalized, now); err != nil {
		return false, err
	}
	return true, nil
}
