package legal

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type internalLegalDocumentService struct {
	db            *gorm.DB
	kind          string
	auditWriter   domainaudit.Appender
	contentBlocks *contentblock.Store
	spiceDB       CollaborationPermissionChecker
	checkpoints   persistencecheckpoint.ContributorFence
	legalOG       OG
}

type legalDocumentLocaleMetadata struct {
	Locale string
	Title  *string
}

type legalDocumentLoadResult struct {
	Document       *contentv1.LocalizedRichTextDocument
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata legalDocumentLocaleMetadata
	Locale         string
	LocaleExists   bool
	LocaleMetadata *legalDocumentLocaleMetadata
	TargetRevision *string
}

type legalDocumentMutationResult struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	ChangedLocales   []string
	Locale           string
	TargetRevision   *string
}

type legalDocumentMetadataInput struct {
	EntityID               string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	Title                  *string
	Contributors           []string
}

func (s internalLegalDocumentService) requireBlockStore() error {
	if s.contentBlocks == nil {
		return errs.InternalMsg(s.kind + " content Block store is not configured")
	}
	if s.spiceDB == nil {
		return errs.InternalMsg(s.kind + " collaboration permission checker is not configured")
	}
	return nil
}

func (s internalLegalDocumentService) loadDocument(
	ctx context.Context,
	entityID string,
	locale string,
	principal *intrav1.CollaborationPrincipal,
) (*legalDocumentLoadResult, error) {
	if err := s.requireBlockStore(); err != nil {
		return nil, err
	}
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return nil, err
	}
	var state legalTargetLocaleState
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := authorizeLegalBlockBootstrap(ctx, tx, s.spiceDB, s.kind, entityID, principal); err != nil {
			return err
		}
		var err error
		state, err = loadLegalTargetLocaleState(
			ctx, tx, s.contentBlocks, s.kind, entityID, locale, false,
		)
		return err
	})
	if err != nil {
		return nil, normalizeLegalContentBlockError(s.kind, err)
	}
	result := &legalDocumentLoadResult{
		Document: state.LocalizedDocument, Snapshot: state.Snapshot,
		SourceLocale: state.SourceLocale,
		SourceMetadata: legalDocumentLocaleMetadata{
			Locale: state.SourceMetadata.Locale, Title: state.SourceMetadata.Title,
		},
		Locale: locale,
	}
	if locale == state.SourceLocale {
		result.LocaleExists = true
		metadata := result.SourceMetadata
		result.LocaleMetadata = &metadata
		return result, nil
	}
	if state.TargetMetadata != nil {
		result.LocaleExists = true
		result.LocaleMetadata = &legalDocumentLocaleMetadata{
			Locale: state.TargetMetadata.Locale, Title: state.TargetMetadata.Title,
		}
		targetRevision := state.TargetRevision
		result.TargetRevision = &targetRevision
	}
	return result, nil
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

func (s internalLegalDocumentService) applyDocumentMutation(
	ctx context.Context,
	entityID string,
	locale string,
	expectedTargetRevision *string,
	request *contentv1.RichTextBlockMutationBatch,
	affectedLocaleValues []*managev1.AIDocumentFieldTarget,
) (*legalDocumentMutationResult, error) {
	if err := s.requireBlockStore(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, errs.Required("batch")
	}
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadLegalContentDocumentID(ctx, s.db, s.kind, entityID)
	if err != nil {
		return nil, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		request,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, errs.InvalidArgument("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		request.Profile,
		locale,
		&storage,
		affectedLocaleValues,
	); err != nil {
		return nil, errs.InvalidArgument("affected_locale_values", err.Error())
	}
	batch, err := contentblock.BatchFromRichTextStorage(documentID, request.Profile, storage)
	if err != nil {
		return nil, normalizeLegalContentBlockError(s.kind, err)
	}
	var result contentblock.Result
	var targetResult legalTargetLocaleMutationResult
	var sourceLocale string
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, stateErr := loadLegalContentDocumentRoot(ctx, tx, s.kind, entityID, true)
		if stateErr != nil {
			return stateErr
		}
		sourceLocale = root.SourceLocale
		if locale != sourceLocale {
			expectedRevision, parseErr := parseLegalContentUUID("batch.expected_revision", request.GetExpectedRevision())
			if parseErr != nil {
				return parseErr
			}
			targetResult, stateErr = applyLegalTargetLocaleMutation(
				ctx,
				tx,
				s.contentBlocks,
				legalTargetLocaleMutationInput{
					EntityType: s.kind, EntityID: entityID, Locale: locale,
					ExpectedDocumentRevision: expectedRevision,
					ExpectedTargetRevision:   expectedTargetRevision,
					Batch:                    batch,
					Now:                      now,
					Fence: legalCollaborationDocumentFence(
						s.checkpoints, s.kind, entityID, request.GetContributorMemberIds(), &batch, false,
					),
				},
			)
			if stateErr != nil {
				return stateErr
			}
			if !targetResult.Changed {
				return nil
			}
			root, rootErr := loadLegalContentDocumentRoot(ctx, tx, s.kind, entityID, false)
			if rootErr != nil {
				return rootErr
			}
			if err := appendLegalRequestTargetLocaleContentAudit(
				ctx, tx, s.auditWriter, s.kind, entityID, root.Version, locale,
			); err != nil {
				return err
			}
			snapshot, snapshotErr := s.contentBlocks.LoadSnapshotInTransaction(
				ctx, tx, documentID, sourceLocale,
			)
			if snapshotErr != nil {
				return snapshotErr
			}
			if err := s.refreshDerivedContentProjectionsWithDB(
				ctx, tx, entityID, snapshot, sourceLocale, now,
			); err != nil {
				return err
			}
			return s.requestSavedOG(ctx, tx, entityID, locale, s.kind+"_target_block_document_saved")
		}
		if expectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source legal room cannot carry a target revision")
		}
		for _, group := range batch.LocaleGroups {
			if group.Locale != sourceLocale {
				return errs.CollaborationMutationRejection(
					intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
					"source legal room mutation must match the current source locale",
				)
			}
		}
		var applyErr error
		result, applyErr = s.contentBlocks.ApplyBatch(
			ctx, tx, batch, legalCollaborationDocumentFence(
				s.checkpoints, s.kind, entityID, request.GetContributorMemberIds(), &batch, false,
			),
		)
		if applyErr != nil {
			return applyErr
		}
		if !result.Changed {
			return nil
		}
		return s.requestSavedOG(
			ctx, tx, entityID, "", s.kind+"_block_document_saved",
		)
	})
	if err != nil {
		return nil, normalizeLegalCollaborationContentBlockError(s.kind, err)
	}
	if locale != sourceLocale {
		targetRevision := targetResult.TargetRevision
		return &legalDocumentMutationResult{
			DocumentRevision: targetResult.DocumentRevision, Changed: targetResult.Changed,
			ChangedLocales: []string{locale},
			Locale:         locale, TargetRevision: &targetRevision,
		}, nil
	}
	return &legalDocumentMutationResult{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged: result.TranslationSourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: locale,
	}, nil
}

func (s internalLegalDocumentService) refreshDerivedContentProjectionsWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	snapshot contentblock.Snapshot,
	sourceLocale string,
	now time.Time,
) error {
	locales := make([]string, 0, len(snapshot.LocaleOverlays)+1)
	seen := make(map[string]struct{}, len(snapshot.LocaleOverlays)+1)
	projectionLocales := append([]string{sourceLocale}, legalSnapshotLocales(snapshot)...)
	var persistedLocales []string
	if err := tx.WithContext(ctx).Table(s.kind+"_translation").
		Where("entity_id = ?", entityID).
		Pluck("locale", &persistedLocales).Error; err != nil {
		return errs.Internal(err)
	}
	projectionLocales = append(projectionLocales, persistedLocales...)
	for _, locale := range projectionLocales {
		if _, exists := seen[locale]; exists {
			continue
		}
		seen[locale] = struct{}{}
		locales = append(locales, locale)
	}
	for _, locale := range locales {
		document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
		if err != nil {
			return err
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil)
		if err != nil {
			return err
		}
		if locale == sourceLocale {
			if err := tx.WithContext(ctx).Table(s.kind+"_history").Where("id = ?", entityID).
				Updates(structured.Fields{
					"content":      projection.HTML,
					"content_text": projection.Text,
					"content_hash": snapshot.SnapshotDigest,
					"view_hash":    snapshot.SnapshotDigest,
					"updated_at":   now,
				}).Error; err != nil {
				return errs.Internal(err)
			}
			continue
		}
		if err := tx.WithContext(ctx).Table(s.kind+"_translation").
			Where("entity_id = ? AND locale = ?", entityID, locale).
			Updates(structured.Fields{
				"content_html": projection.HTML,
				"content_text": projection.Text,
			}).Error; err != nil {
			return errs.Internal(err)
		}
	}
	return nil
}

func legalSnapshotLocales(snapshot contentblock.Snapshot) []string {
	locales := make([]string, 0, len(snapshot.LocaleOverlays))
	for _, overlay := range snapshot.LocaleOverlays {
		if locale := strings.TrimSpace(overlay.Locale); locale != "" {
			locales = append(locales, locale)
		}
	}
	return locales
}

func (s internalLegalDocumentService) updateDocumentMetadata(
	ctx context.Context,
	input legalDocumentMetadataInput,
) (*legalDocumentMutationResult, bool, error) {
	if err := s.requireBlockStore(); err != nil {
		return nil, false, err
	}
	documentID, err := loadLegalContentDocumentID(ctx, s.db, s.kind, input.EntityID)
	if err != nil {
		return nil, false, err
	}
	expectedRevision, err := parseLegalContentUUID("expected_revision", input.ExpectedRevision)
	if err != nil {
		return nil, false, err
	}
	input.Locale, err = canonicalLegalLocale(input.Locale)
	if err != nil {
		return nil, false, err
	}
	var advance contentblock.AdvanceResult
	var targetResult legalTargetLocaleMutationResult
	var sourceLocale string
	var sourceLocaleDocument bool
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, stateErr := loadLegalContentDocumentRoot(ctx, tx, s.kind, input.EntityID, true)
		if stateErr != nil {
			return stateErr
		}
		sourceLocale = root.SourceLocale
		if input.Locale != sourceLocale {
			contributors := make([]uuid.UUID, 0, len(input.Contributors))
			for _, contributor := range input.Contributors {
				parsed, parseErr := parseLegalContentUUID("contributor_member_ids", contributor)
				if parseErr != nil {
					return parseErr
				}
				contributors = append(contributors, parsed)
			}
			targetResult, stateErr = applyLegalTargetLocaleMutation(
				ctx,
				tx,
				s.contentBlocks,
				legalTargetLocaleMutationInput{
					EntityType: s.kind, EntityID: input.EntityID, Locale: input.Locale,
					ExpectedDocumentRevision: expectedRevision,
					ExpectedTargetRevision:   input.ExpectedTargetRevision,
					Batch: contentblock.Batch{
						DocumentID: documentID, ExpectedRevision: expectedRevision,
						ContributorMemberIDs: contributors,
					},
					SetTitle: true, Title: input.Title, Now: now,
					Fence: legalCollaborationDocumentFence(
						s.checkpoints, s.kind, input.EntityID, input.Contributors, nil, false,
					),
				},
			)
			if stateErr != nil || !targetResult.Changed {
				return stateErr
			}
			root, rootErr := loadLegalContentDocumentRoot(ctx, tx, s.kind, input.EntityID, false)
			if rootErr != nil {
				return rootErr
			}
			if err := appendLegalRequestTargetLocaleContentAudit(
				ctx, tx, s.auditWriter, s.kind, input.EntityID, root.Version, input.Locale,
			); err != nil {
				return err
			}
			return s.requestSavedOG(
				ctx, tx, input.EntityID, input.Locale, s.kind+"_locale_title_saved",
			)
		}
		if input.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source legal room cannot carry a target revision")
		}
		var advanceErr error
		advance, advanceErr = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			legalCollaborationDocumentFence(
				s.checkpoints, s.kind, input.EntityID, input.Contributors, nil, true,
			),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				if sourceLocale != input.Locale {
					return contentblock.MetadataEffect{}, errs.FailedPrecondition("legal source locale changed; reload before saving")
				}
				sourceLocaleDocument = true
				changed, mutationErr := s.updateLocaleTitle(ctx, tx, input.EntityID, input.Locale, input.Title, sourceLocale, now)
				return contentblock.MetadataEffect{
					Changed: changed, AffectsTranslationSource: changed && sourceLocaleDocument,
				}, mutationErr
			},
		)
		if advanceErr != nil {
			return advanceErr
		}
		if advance.Changed {
			return s.requestSavedOG(
				ctx, tx, input.EntityID, input.Locale, s.kind+"_locale_title_saved",
			)
		}
		return nil
	})
	if err != nil {
		return nil, false, normalizeLegalCollaborationContentBlockError(s.kind, err)
	}
	if input.Locale != sourceLocale {
		targetRevision := targetResult.TargetRevision
		return &legalDocumentMutationResult{
			DocumentRevision: targetResult.DocumentRevision, Changed: targetResult.Changed,
			ChangedLocales: []string{input.Locale},
			Locale:         input.Locale, TargetRevision: &targetRevision,
		}, false, nil
	}
	return &legalDocumentMutationResult{
		DocumentRevision: advance.DocumentRevision.String(), Changed: advance.Changed,
		SourceChanged:  advance.TranslationSourceChanged,
		ChangedLocales: []string{input.Locale},
		Locale:         input.Locale,
	}, sourceLocaleDocument, nil
}

func (s internalLegalDocumentService) requestSavedOG(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	locale string,
	reason string,
) error {
	if s.legalOG == nil {
		return errs.InternalMsg(s.kind + " OG runtime is not configured")
	}
	return s.legalOG.RequestSaved(ctx, tx, s.kind, entityID, locale, false, reason)
}

func (s internalLegalDocumentService) updateLocaleTitle(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	locale string,
	title *string,
	sourceLocale string,
	now time.Time,
) (bool, error) {
	if title == nil {
		return false, nil
	}
	normalized := strings.TrimSpace(*title)
	if normalized == "" {
		return false, errs.InvalidArgument("title", "must not be empty")
	}
	if locale == sourceLocale {
		root, err := loadLegalContentDocumentRoot(ctx, tx, s.kind, entityID, false)
		if err != nil {
			return false, err
		}
		if root.Title == normalized {
			return false, nil
		}
		if err := tx.WithContext(ctx).Table(s.kind+"_history").Where("id = ?", entityID).
			Updates(structured.Fields{"title": normalized, "updated_at": now}).Error; err != nil {
			return false, errs.Internal(err)
		}
		return true, nil
	}
	return false, errs.FailedPrecondition("target legal locale metadata is read-only")
}
