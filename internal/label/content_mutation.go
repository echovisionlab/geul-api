package label

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ApplyLabelBlockBatchCommand applies a collaboration room's Block batch to
// the Label document in Locale. A target-locale room names the target
// revision it read.
type ApplyLabelBlockBatchCommand struct {
	LabelID                string
	Locale                 string
	ExpectedTargetRevision *string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
}

// BlockBatchResult is an applied collaboration batch.
type BlockBatchResult struct {
	Locale         string
	Content        contentblock.Result
	TargetRevision *string
	SourceLocale   string
	LocaleExists   bool
}

// ApplyLabelBlockBatch applies the batch on behalf of its contributors,
// whose current collaboration authority the content fence rechecks.
func (s *Service) ApplyLabelBlockBatch(ctx context.Context, command ApplyLabelBlockBatchCommand) (BlockBatchResult, error) {
	locale, err := normalizeLabelDocumentLocale(command.Locale)
	if err != nil {
		return BlockBatchResult{}, err
	}
	input := command.Batch
	if input == nil {
		return BlockBatchResult{}, domainerr.Required("batch")
	}
	doc, err := loadLabelContentDocumentID(ctx, s.db, command.LabelID)
	if err != nil {
		return BlockBatchResult{}, err
	}
	contributors := input.GetContributorMemberIds()
	return mutation.Run(ctx, s.mutations, mutation.Spec[BlockBatchResult]{
		Access: s.contributorAccess(contributors),
		Change: func(ctx context.Context, tx *gorm.DB) (BlockBatchResult, error) {
			domain, err := internalLabelCollaborationContentFence(s.checkpoints, command.LabelID, contributors)(ctx, tx, doc)
			if err != nil {
				return BlockBatchResult{}, err
			}
			batch := labelRoomBatch{
				labelID: command.LabelID, locale: locale, documentID: doc, domain: domain,
				expectedTargetRevision: command.ExpectedTargetRevision, input: input,
			}
			if locale != domain.SourceLocale {
				return s.applyLabelTargetRoomBatch(ctx, tx, batch, command.AffectedLocaleValues)
			}
			return s.applyLabelSourceRoomBatch(ctx, tx, batch, command.AffectedLocaleValues)
		},
		Audit: func(result BlockBatchResult) mutation.Audit {
			if !result.Content.Changed {
				return mutation.Unchanged()
			}
			return labelRequestLocaleAudit(command.LabelID, locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, result BlockBatchResult) error {
			if !result.Content.Changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildLabelLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID:             command.LabelID,
				Fields:               []string{"content"},
				DocumentRevision:     result.Content.DocumentRevision.String(),
				Contributors:         contributors,
				Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale:               locale,
				LocaleDeleted:        !result.LocaleExists,
				TargetRevision:       result.TargetRevision,
				DocumentStateChanged: locale == result.SourceLocale,
			}))
		},
	})
}

// contributorAccess authorizes a collaboration save by its contributors.
// The content fence rechecks their current room authority under the Label
// lock.
func (s *Service) contributorAccess(contributors []string) mutation.Access {
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		return s.translation.RequireDocumentContributors(ctx, tx, contributors)
	})
}

// labelRoomBatch is a collaboration batch under the locked content fence.
type labelRoomBatch struct {
	labelID                string
	locale                 string
	documentID             uuid.UUID
	domain                 contentblock.DomainContext
	expectedTargetRevision *string
	input                  *contentv1.RichTextBlockMutationBatch
}

// storage flattens the batch and restores the locale values it affects.
func (b labelRoomBatch) storage(affectedLocaleValues []*managev1.AIDocumentFieldTarget) (contentv1.ContentStorageMutationBatch, error) {
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(b.input, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE)
	if err != nil {
		return storage, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(b.input.GetProfile(), b.locale, &storage, affectedLocaleValues); err != nil {
		return storage, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	return storage, nil
}

func (b labelRoomBatch) fence() contentblock.DomainFence {
	return contentblock.LockedFence(labelEntityName, b.documentID, b.domain)
}

// applyLabelTargetRoomBatch applies a target-locale room's batch, which may
// change only that locale's values of the shared Blocks.
func (s *Service) applyLabelTargetRoomBatch(ctx context.Context, tx *gorm.DB, b labelRoomBatch, affectedLocaleValues []*managev1.AIDocumentFieldTarget) (BlockBatchResult, error) {
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, b.documentID, b.domain.SourceLocale)
	if err != nil {
		return BlockBatchResult{}, normalizeLabelContentBlockError(err)
	}
	if err := validateLabelTargetProtoMutation(b.input, snapshot, b.locale); err != nil {
		return BlockBatchResult{}, err
	}
	storage, err := b.storage(affectedLocaleValues)
	if err != nil {
		return BlockBatchResult{}, err
	}
	batch, err := contentblock.BatchFromRichTextStorage(b.documentID, b.input.GetProfile(), storage)
	if err != nil {
		return BlockBatchResult{}, normalizeLabelContentBlockError(err)
	}
	target, err := applyLabelTargetLocaleMutation(ctx, tx, s.contentBlocks, labelTargetLocaleMutationInput{
		LabelID: b.labelID, Locale: b.locale, ExpectedDocumentRevision: batch.ExpectedRevision,
		ExpectedTargetRevision: b.expectedTargetRevision, Batch: batch, Fence: b.fence(),
	})
	if err != nil {
		return BlockBatchResult{}, normalizeLabelContentBlockError(err)
	}
	return BlockBatchResult{
		Locale: b.locale, Content: target.Content, TargetRevision: &target.TargetRevision,
		SourceLocale: b.domain.SourceLocale, LocaleExists: true,
	}, nil
}

// applyLabelSourceRoomBatch applies the source room's batch and refreshes
// the OG image when the translation source changed.
func (s *Service) applyLabelSourceRoomBatch(ctx context.Context, tx *gorm.DB, b labelRoomBatch, affectedLocaleValues []*managev1.AIDocumentFieldTarget) (BlockBatchResult, error) {
	storage, err := b.storage(affectedLocaleValues)
	if err != nil {
		return BlockBatchResult{}, err
	}
	if b.expectedTargetRevision != nil {
		return BlockBatchResult{}, domainerr.InvalidField("expected_target_revision", "source Label mutation cannot carry a target revision")
	}
	batch, err := contentblock.BatchFromRichTextStorage(b.documentID, b.input.GetProfile(), storage)
	if err != nil {
		return BlockBatchResult{}, normalizeLabelContentBlockError(err)
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != b.domain.SourceLocale {
			return BlockBatchResult{}, domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Label source room mutation must match the current source locale",
			)
		}
	}
	result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, b.fence())
	if err != nil {
		return BlockBatchResult{}, normalizeLabelContentBlockError(err)
	}
	if result.TranslationSourceChanged {
		if _, err := s.runtime.RequestCurrentWithDB(ctx, tx, b.labelID, "label_source_document_saved"); err != nil {
			return BlockBatchResult{}, err
		}
	}
	return BlockBatchResult{Locale: b.locale, Content: result, SourceLocale: b.domain.SourceLocale, LocaleExists: true}, nil
}

func validateLabelTargetProtoMutation(
	input *contentv1.RichTextBlockMutationBatch,
	snapshot contentblock.Snapshot,
	locale string,
) error {
	for _, group := range input.GetLocaleMutationGroups() {
		if group.GetLocale() != locale {
			return domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Label target locale mutation must match the authenticated room locale",
			)
		}
	}
	currentKinds := make(map[string]string, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		currentKinds[block.ID.String()] = block.Kind
	}
	baseMutations := input.GetBaseMutations()
	if len(baseMutations) == 0 {
		return nil
	}
	upsert := baseMutations[0].GetUpsert()
	if upsert == nil || upsert.GetNode().GetBlock() == nil {
		return domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Label target locale cannot mutate the shared Block graph",
		)
	}
	block := upsert.GetNode().GetBlock()
	if block.GetFile() != nil {
		return domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_FILE_RELATION_FORBIDDEN,
			"Label target locale cannot attach a File Block",
		)
	}
	currentKind, exists := currentKinds[block.GetId()]
	if !exists || currentKind != labelProtoBlockKind(block) {
		return domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Label target locale cannot insert or replace a shared Block",
		)
	}
	return domainerr.CollaborationRejection(
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_SHARED_FIELD_FORBIDDEN,
		"Label target locale cannot change source-owned Block fields",
	)
}

func labelProtoBlockKind(block *contentv1.RichTextBlock) string {
	if block == nil {
		return ""
	}
	message := block.ProtoReflect()
	oneof := message.Descriptor().Oneofs().ByName("value")
	if oneof == nil {
		return ""
	}
	field := message.WhichOneof(oneof)
	if field == nil {
		return ""
	}
	return string(field.Name())
}

// UpdateLabelLocaleMetadataCommand saves the title of the Label's source
// locale from its collaboration room.
type UpdateLabelLocaleMetadataCommand struct {
	LabelID                string
	Locale                 string
	Title                  *string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	ContributorMemberIDs   []string
}

// labelSourceTitle is a source title save under the content fence.
type labelSourceTitle struct {
	labelID    string
	locale     string
	name       string
	documentID uuid.UUID
	expected   uuid.UUID
}

// saveLabelSourceTitle advances the document revision with the new title and
// refreshes the OG image when the translation source changed.
func (s *Service) saveLabelSourceTitle(ctx context.Context, tx *gorm.DB, title labelSourceTitle, domain contentblock.DomainContext) (TitleUpdate, error) {
	advanced, err := s.contentBlocks.AdvanceRevision(ctx, tx, contentblock.AdvanceInput{DocumentID: title.documentID, ExpectedRevision: title.expected}, contentblock.LockedFence(labelEntityName, title.documentID, domain), func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
		if err := saveLabelSourceLocaleDocumentState(ctx, tx, title.labelID, domain.SourceLocale, translationLocaleDocumentSaveInput{Title: &title.name, OverwriteNullFields: true, Now: time.Now().UTC()}); err != nil {
			return contentblock.MetadataEffect{}, err
		}
		if err := touchLabelRootUpdatedAt(ctx, tx, title.labelID, time.Now().UTC()); err != nil {
			return contentblock.MetadataEffect{}, err
		}
		return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
	})
	if err != nil {
		return TitleUpdate{}, normalizeLabelContentBlockError(err)
	}
	update := TitleUpdate{Locale: title.locale, Advance: advanced}
	if !advanced.Changed {
		return update, nil
	}
	update.ChangedLocales = []string{domain.SourceLocale}
	if advanced.TranslationSourceChanged {
		_, err = s.runtime.RequestCurrentWithDB(ctx, tx, title.labelID, "label_source_title_saved")
	}
	return update, err
}

// TitleUpdate is a saved source title.
type TitleUpdate struct {
	Locale         string
	Advance        contentblock.AdvanceResult
	ChangedLocales []string
}

// UpdateLabelLocaleMetadata saves the source title; a target locale room
// may not change it.
func (s *Service) UpdateLabelLocaleMetadata(ctx context.Context, command UpdateLabelLocaleMetadataCommand) (TitleUpdate, error) {
	locale, err := normalizeLabelDocumentLocale(command.Locale)
	if err != nil {
		return TitleUpdate{}, err
	}
	if command.Title == nil || strings.TrimSpace(*command.Title) == "" {
		return TitleUpdate{}, domainerr.InvalidField("title", "must not be empty")
	}
	expected, err := uuid.Parse(command.ExpectedRevision)
	if err != nil {
		return TitleUpdate{}, domainerr.InvalidField("expected_revision", "must be a UUID")
	}
	doc, err := loadLabelContentDocumentID(ctx, s.db, command.LabelID)
	if err != nil {
		return TitleUpdate{}, err
	}
	title := labelSourceTitle{labelID: command.LabelID, locale: locale, name: strings.TrimSpace(*command.Title), documentID: doc, expected: expected}
	return mutation.Run(ctx, s.mutations, mutation.Spec[TitleUpdate]{
		Access: s.contributorAccess(command.ContributorMemberIDs),
		Change: func(ctx context.Context, tx *gorm.DB) (TitleUpdate, error) {
			domain, err := internalLabelCollaborationContentFence(s.checkpoints, command.LabelID, command.ContributorMemberIDs)(ctx, tx, doc)
			if err != nil {
				return TitleUpdate{}, err
			}
			if locale != domain.SourceLocale {
				return TitleUpdate{}, domainerr.CollaborationRejection(
					intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
					"Label title is source-owned and cannot be changed from a target locale room",
				)
			}
			if command.ExpectedTargetRevision != nil {
				return TitleUpdate{}, domainerr.InvalidField("expected_target_revision", "source Label metadata mutation cannot carry a target revision")
			}
			return s.saveLabelSourceTitle(ctx, tx, title, domain)
		},
		Audit: func(update TitleUpdate) mutation.Audit {
			if !update.Advance.Changed {
				return mutation.Unchanged()
			}
			return labelRequestLocaleAudit(command.LabelID, update.ChangedLocales[0], sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, update TitleUpdate) error {
			if !update.Advance.Changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildLabelLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID:             command.LabelID,
				Fields:               []string{"title"},
				DocumentRevision:     update.Advance.DocumentRevision.String(),
				Contributors:         command.ContributorMemberIDs,
				Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale:               locale,
				DocumentStateChanged: true,
			}))
		},
	})
}
