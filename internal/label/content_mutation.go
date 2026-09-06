package label

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type labelBlockApplyResult struct {
	Content        contentblock.Result
	TargetRevision *string
	SourceLocale   string
	LocaleExists   bool
}

func (s *InternalLabelService) applyLabelBlockBatch(ctx context.Context, id, locale string, expectedTargetRevision *string, input *contentv1.RichTextBlockMutationBatch, affectedLocaleValues []*managev1.AIDocumentFieldTarget) (labelBlockApplyResult, error) {
	if s.contentBlocks == nil {
		return labelBlockApplyResult{}, errs.Internal(errors.New("label content Block store is not configured"))
	}
	if input == nil {
		return labelBlockApplyResult{}, errs.Required("batch")
	}
	locale, err := normalizeLabelDocumentLocale(locale)
	if err != nil {
		return labelBlockApplyResult{}, err
	}
	doc, err := loadLabelContentDocumentID(ctx, s.db, id)
	if err != nil {
		return labelBlockApplyResult{}, err
	}
	var out labelBlockApplyResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.translation.RequireDocumentContributors(ctx, tx, input.GetContributorMemberIds()); err != nil {
			return err
		}
		domain, err := internalLabelCollaborationContentFence(s.checkpoints, id, input.GetContributorMemberIds())(ctx, tx, doc)
		if err != nil {
			return err
		}
		out.SourceLocale = domain.SourceLocale
		lockedFence := lockedLabelContentFence(doc, domain)
		if locale != domain.SourceLocale {
			snapshot, loadErr := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, doc, domain.SourceLocale)
			if loadErr != nil {
				return normalizeLabelContentBlockError(loadErr)
			}
			if validationErr := validateLabelTargetProtoMutation(input, snapshot, locale); validationErr != nil {
				return validationErr
			}
		}
		storage, flattenErr := contentv1.FlattenRichTextMutationBatchStorage(input, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE)
		if flattenErr != nil {
			return errs.InvalidArgument("batch", flattenErr.Error())
		}
		if restoreErr := contentblock.RestoreRichTextAffectedLocaleValues(input.GetProfile(), locale, &storage, affectedLocaleValues); restoreErr != nil {
			return errs.InvalidArgument("affected_locale_values", restoreErr.Error())
		}
		if locale != domain.SourceLocale {
			batch, conversionErr := contentblock.BatchFromRichTextStorage(doc, input.GetProfile(), storage)
			if conversionErr != nil {
				return normalizeLabelContentBlockError(conversionErr)
			}
			target, err := applyLabelTargetLocaleMutation(ctx, tx, s.contentBlocks, labelTargetLocaleMutationInput{
				LabelID: id, Locale: locale, ExpectedDocumentRevision: batch.ExpectedRevision,
				ExpectedTargetRevision: expectedTargetRevision, Batch: batch,
				AllowCreate: false, Fence: lockedFence,
			})
			if err != nil {
				return normalizeLabelContentBlockError(err)
			}
			out.Content = target.Content
			out.TargetRevision = &target.TargetRevision
			out.LocaleExists = true
			if target.Content.Changed {
				return appendLabelRequestLocaleAudit(
					ctx, tx, s.auditWriter, id, locale,
					sharedtelemetry.AuditItemOperationUpdated,
				)
			}
			return nil
		}
		if expectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Label mutation cannot carry a target revision")
		}
		batch, err := contentblock.BatchFromRichTextStorage(doc, input.GetProfile(), storage)
		if err != nil {
			return normalizeLabelContentBlockError(err)
		}
		for _, group := range batch.LocaleGroups {
			if group.Locale != domain.SourceLocale {
				return errs.CollaborationMutationRejection(
					intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
					"Label source room mutation must match the current source locale",
				)
			}
		}
		result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, lockedFence)
		if err != nil {
			return normalizeLabelContentBlockError(err)
		}
		out.Content = result
		out.LocaleExists = true
		if result.Changed {
			if err := appendLabelRequestLocaleAudit(
				ctx, tx, s.auditWriter, id, domain.SourceLocale,
				sharedtelemetry.AuditItemOperationUpdated,
			); err != nil {
				return err
			}
		}
		if !result.TranslationSourceChanged {
			return nil
		}
		_, err = s.runtime.RequestCurrentWithDB(ctx, tx, id, "label_source_document_saved")
		return err
	})
	return out, err
}

func validateLabelTargetProtoMutation(
	input *contentv1.RichTextBlockMutationBatch,
	snapshot contentblock.Snapshot,
	locale string,
) error {
	for _, group := range input.GetLocaleMutationGroups() {
		if group.GetLocale() != locale {
			return errs.CollaborationMutationRejection(
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
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Label target locale cannot mutate the shared Block graph",
		)
	}
	block := upsert.GetNode().GetBlock()
	if block.GetFile() != nil {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_FILE_RELATION_FORBIDDEN,
			"Label target locale cannot attach a File Block",
		)
	}
	currentKind, exists := currentKinds[block.GetId()]
	if !exists || currentKind != labelProtoBlockKind(block) {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Label target locale cannot insert or replace a shared Block",
		)
	}
	return errs.CollaborationMutationRejection(
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

type labelTitleUpdateResult struct {
	Advance        contentblock.AdvanceResult
	ChangedLocales []string
}

func (s *InternalLabelService) updateLabelLocaleTitle(ctx context.Context, id, locale string, title *string, revision string, expectedTargetRevision *string, contributors []string) (labelTitleUpdateResult, error) {
	if s.contentBlocks == nil {
		return labelTitleUpdateResult{}, errs.Internal(errors.New("label content Block store is not configured"))
	}
	if title == nil || strings.TrimSpace(*title) == "" {
		return labelTitleUpdateResult{}, errs.InvalidArgument("title", "must not be empty")
	}
	expected, err := uuid.Parse(revision)
	if err != nil {
		return labelTitleUpdateResult{}, errs.InvalidArgument("expected_revision", "must be a UUID")
	}
	doc, err := loadLabelContentDocumentID(ctx, s.db, id)
	if err != nil {
		return labelTitleUpdateResult{}, err
	}
	name := strings.TrimSpace(*title)
	var out labelTitleUpdateResult
	var sourceLocale string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.translation.RequireDocumentContributors(ctx, tx, contributors); err != nil {
			return err
		}
		domain, err := internalLabelCollaborationContentFence(s.checkpoints, id, contributors)(ctx, tx, doc)
		if err != nil {
			return err
		}
		if locale != domain.SourceLocale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
				"Label title is source-owned and cannot be changed from a target locale room",
			)
		}
		if expectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Label metadata mutation cannot carry a target revision")
		}
		advanced, err := s.contentBlocks.AdvanceRevision(ctx, tx, contentblock.AdvanceInput{DocumentID: doc, ExpectedRevision: expected}, lockedLabelContentFence(doc, domain), func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			sourceLocale = domain.SourceLocale
			if err := saveLabelSourceLocaleDocumentState(ctx, tx, id, sourceLocale, translationLocaleDocumentSaveInput{Title: &name, OverwriteNullFields: true, Now: time.Now().UTC()}); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if err := touchLabelRootUpdatedAt(ctx, tx, id, time.Now().UTC()); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
		})
		if err != nil {
			return normalizeLabelContentBlockError(err)
		}
		out.Advance = advanced
		if !advanced.Changed {
			return nil
		}
		out.ChangedLocales = []string{sourceLocale}
		if err := appendLabelRequestLocaleAudit(
			ctx, tx, s.auditWriter, id, sourceLocale,
			sharedtelemetry.AuditItemOperationUpdated,
		); err != nil {
			return err
		}
		if !advanced.TranslationSourceChanged {
			return nil
		}
		_, err = s.runtime.RequestCurrentWithDB(ctx, tx, id, "label_source_title_saved")
		return err
	})
	return out, err
}
