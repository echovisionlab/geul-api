package work

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *WorkService) ListWorkVersions(
	ctx context.Context,
	req *connect.Request[managev1.ListWorkVersionsRequest],
) (*connect.Response[managev1.ListWorkVersionsResponse], error) {
	var root model.Work
	if err := s.db.WithContext(ctx).Select("id", "status").Take(&root, "id = ?", req.Msg.WorkId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("work", req.Msg.WorkId)
		}
		return nil, errs.Internal(err)
	}
	if err := requireWorkPermission(ctx, s.spiceDB, root, policyv1.Work.View, workAuthorizationRead); err != nil {
		return nil, err
	}

	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	var total int64
	if err := s.db.WithContext(ctx).
		Model(&model.WorkVersion{}).
		Where("work_id = ?", req.Msg.WorkId).
		Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	var versions []model.WorkVersion
	if err := s.db.WithContext(ctx).
		Where("work_id = ?", req.Msg.WorkId).
		Order("version DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&versions).Error; err != nil {
		return nil, errs.Internal(err)
	}

	contributorLists := make([][]string, len(versions))
	for index := range versions {
		contributorLists[index] = versions[index].ContributorMemberIDs
	}
	contributorNames, err := resolveVersionContributorNames(ctx, s.db, contributorLists...)
	if err != nil {
		return nil, errs.Internal(err)
	}

	protoVersions := make([]*managev1.WorkVersion, len(versions))
	for index := range versions {
		protoVersions[index], err = s.toProtoWorkVersion(&versions[index], contributorNames)
		if err != nil {
			return nil, errs.FailedPrecondition("Work Version typed snapshot is invalid")
		}
	}

	return connect.NewResponse(&managev1.ListWorkVersionsResponse{
		Versions: protoVersions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// RestoreWorkVersion replaces the full source-owned graph and locale metadata
// from one immutable checkpoint. Existing target locale rows and translation
// jobs remain unchanged, including when the checkpoint switches source-locale
// authority. Global Work fields remain current.
func (s *WorkService) RestoreWorkVersion(
	ctx context.Context,
	req *connect.Request[managev1.RestoreWorkVersionRequest],
) (*connect.Response[managev1.Work], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Work content Block store is not configured")
	}

	version, err := s.loadWorkVersionForRestore(ctx, req.Msg.VersionId, req.Msg.WorkId)
	if err != nil {
		return nil, err
	}
	versionEnvelope, versionDocument, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, errs.FailedPrecondition("Work Version typed snapshot is invalid")
	}
	if versionEnvelope.Title == nil || strings.TrimSpace(*versionEnvelope.Title) == "" {
		return nil, errs.FailedPrecondition("Work Version source title is invalid")
	}

	now := time.Now().UTC()
	var finalSnapshot contentblock.Snapshot
	var blocksChanged bool
	var titleChanged bool
	var summaryChanged bool
	var restoredChanged bool
	var sourceLocaleSwitched bool
	var restoreContributors []string
	if user := auth.GetUser(ctx); user != nil {
		restoreContributors = []string{user.MemberID.String()}
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, loadErr := loadWorkContentDocumentID(ctx, tx, version.WorkID)
		if loadErr != nil {
			return loadErr
		}
		work, lockErr := lockWorkForContentDocument(ctx, tx, documentID)
		if lockErr != nil {
			return lockErr
		}
		if work.ID != version.WorkID {
			return errs.FailedPrecondition("Work content document changed during version restore")
		}
		if _, authErr := requireLockedWorkPermission(ctx, tx, s.spiceDB, version.WorkID, policyv1.Work.Edit, workAuthorizationMutation); authErr != nil {
			return authErr
		}

		currentSource, sourceErr := LoadRequiredSourceLocaleMetadata(ctx, tx, version.WorkID)
		if sourceErr != nil {
			return sourceErr
		}
		currentSnapshot, snapshotErr := s.contentBlocks.LoadSnapshotInTransaction(
			ctx,
			tx,
			documentID,
			currentSource.Locale,
		)
		if snapshotErr != nil {
			return normalizeWorkContentBlockError(snapshotErr)
		}
		if currentSource.Locale != versionEnvelope.SourceLocale {
			switchErr := switchWorkVersionRestoreSourceLocale(
				ctx, tx, s.contentBlocks, version.WorkID, versionEnvelope.SourceLocale,
				currentSnapshot.Document.Revision, currentSource.Locale, now,
			)
			if switchErr != nil {
				return switchErr
			}
			sourceLocaleSwitched = true
			currentSource, sourceErr = LoadRequiredSourceLocaleMetadata(ctx, tx, version.WorkID)
			if sourceErr != nil {
				return sourceErr
			}
			currentSnapshot, snapshotErr = s.contentBlocks.LoadSnapshotInTransaction(
				ctx, tx, documentID, currentSource.Locale,
			)
			if snapshotErr != nil {
				return normalizeWorkContentBlockError(snapshotErr)
			}
		}
		titleChanged = !nullableStringEqual(currentSource.Title, versionEnvelope.Title)
		summaryChanged = !nullableStringEqual(currentSource.Summary, versionEnvelope.Summary)
		unavailable, unavailableErr := s.runtime.LoadUnavailableVersionAttachmentKinds(ctx, tx, versionDocument)
		if unavailableErr != nil {
			return unavailableErr
		}
		replaceInput, replaceErr := contentblock.ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
			documentID,
			currentSnapshot.Document.Revision,
			versionDocument,
			unavailable,
		)
		if replaceErr != nil {
			return normalizeWorkContentBlockError(replaceErr)
		}
		replaceResult, replaceErr := s.contentBlocks.ReplaceSnapshot(
			ctx,
			tx,
			replaceInput,
			lockedWorkContentFence(),
		)
		if replaceErr != nil {
			return normalizeWorkContentBlockError(replaceErr)
		}
		blocksChanged = replaceResult.Changed
		finalRevision := replaceResult.DocumentRevision

		metadataChanged := titleChanged || summaryChanged
		if metadataChanged && !blocksChanged {
			_, advanceErr := s.contentBlocks.AdvanceRevision(
				ctx,
				tx,
				contentblock.AdvanceInput{
					DocumentID:       documentID,
					ExpectedRevision: finalRevision,
				},
				lockedWorkContentFence(),
				func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
					if updateErr := updateWorkVersionSourceMetadata(
						ctx,
						tx,
						version.WorkID,
						versionEnvelope.SourceLocale,
						versionEnvelope.Title,
						versionEnvelope.Summary,
						now,
					); updateErr != nil {
						return contentblock.MetadataEffect{}, updateErr
					}
					return contentblock.MetadataEffect{
						Changed:                  true,
						AffectsTranslationSource: true,
					}, nil
				},
			)
			if advanceErr != nil {
				return normalizeWorkContentBlockError(advanceErr)
			}
		} else if metadataChanged {
			if updateErr := updateWorkVersionSourceMetadata(
				ctx,
				tx,
				version.WorkID,
				versionEnvelope.SourceLocale,
				versionEnvelope.Title,
				versionEnvelope.Summary,
				now,
			); updateErr != nil {
				return updateErr
			}
		}

		restoredChanged = sourceLocaleSwitched || blocksChanged || metadataChanged
		if !restoredChanged {
			return nil
		}
		var loadFinalErr error
		finalSnapshot, loadFinalErr = s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, currentSource.Locale,
		)
		if loadFinalErr != nil {
			return normalizeWorkContentBlockError(loadFinalErr)
		}

		if updateErr := tx.WithContext(ctx).
			Model(&model.Work{}).
			Where("id = ?", version.WorkID).
			UpdateColumn("updated_at", now).Error; updateErr != nil {
			return updateErr
		}
		if _, ogErr := s.runtime.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
			version.WorkID, versionEnvelope.SourceLocale, false, "work_version_restored",
		); ogErr != nil {
			return ogErr
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditWorkUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkVersionRestoreAuditRecord(metadata, version.WorkID, version.ID)
			},
		)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, normalizeWorkContentBlockError(err)
	}

	if restoredChanged {
		event := buildWorkSourceContentUpdatedEvent(
			version.WorkID,
			titleChanged,
			summaryChanged,
			blocksChanged,
			finalSnapshot.Document.Revision.String(),
			restoreContributors,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
			finalSnapshot.SourceLocale,
			true,
			nil,
			true,
		)
		if sourceLocaleSwitched {
			if event == nil {
				event = buildContentUpdatedEvent(
					managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
					version.WorkID,
					managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
					[]*managev1.ContentUpdatedField{{
						Path: "settings.source_locale", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION,
					}},
					false,
					finalSnapshot.Document.Revision.String(),
					restoreContributors,
				)
			} else {
				event.ChangedFields = append(event.ChangedFields, &managev1.ContentUpdatedField{
					Path: "settings.source_locale", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION,
				})
			}
		}
		publishWorkContentUpdated(ctx, s.asyncPublisher, event)
	}

	work, err := s.loadRestoredWork(ctx, version.WorkID)
	if err != nil {
		return nil, err
	}
	return s.workResponseWithReadyOg(ctx, &work, s.getWorkFeaturedImageAsset(ctx, work.ID))
}

func (s *WorkService) loadWorkVersionForRestore(
	ctx context.Context,
	versionID string,
	expectedWorkID string,
) (model.WorkVersion, error) {
	var version model.WorkVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", versionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.WorkVersion{}, errs.NotFound("work_version", versionID)
		}
		return model.WorkVersion{}, errs.Internal(err)
	}
	if expectedWorkID != "" && version.WorkID != expectedWorkID {
		return model.WorkVersion{}, errs.NotFound("work_version", versionID)
	}
	return version, nil
}

func updateWorkVersionSourceMetadata(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
	sourceLocale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	result := tx.WithContext(ctx).
		Table("work_translation").
		Where(
			"entity_id = ? AND locale = ?",
			workID,
			sourceLocale,
		).
		Updates(structured.Fields{
			"title":      title,
			"summary":    summary,
			"updated_at": now,
		})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("Work source locale changed during version restore")
	}
	return nil
}

func (s *WorkService) loadRestoredWork(ctx context.Context, workID string) (model.Work, error) {
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", workID).Error; err != nil {
		return model.Work{}, errs.Internal(err)
	}
	source, err := LoadRequiredSourceLocaleMetadata(ctx, s.db, work.ID)
	if err != nil {
		return model.Work{}, err
	}
	if source.Title == nil {
		return model.Work{}, errs.FailedPrecondition("Work source title is not initialized")
	}
	work.Title = *source.Title
	work.Summary = cloneOptionalString(source.Summary)
	return work, nil
}

func (s *WorkService) toProtoWorkVersion(
	version *model.WorkVersion,
	contributorNames map[string]string,
) (*managev1.WorkVersion, error) {
	if version == nil {
		return nil, errors.New("work Version is required")
	}
	envelope, _, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, err
	}
	if envelope.Title == nil || strings.TrimSpace(*envelope.Title) == "" {
		return nil, errors.New("work Version source title is invalid")
	}
	digest := sha256.Sum256(version.ContentSnapshot)
	canonicalHash := fmt.Sprintf("%x", digest[:])
	contributors, err := toProtoVersionContributors(version.ContributorMemberIDs, contributorNames)
	if err != nil {
		return nil, err
	}
	return &managev1.WorkVersion{
		Id:            version.ID,
		Version:       version.Version,
		SourceLocale:  envelope.SourceLocale,
		Title:         *envelope.Title,
		Summary:       cloneOptionalString(envelope.Summary),
		CanonicalHash: canonicalHash,
		CreatedAt:     timestamppb.New(version.CreatedAt),
		Contributors:  contributors,
	}, nil
}
