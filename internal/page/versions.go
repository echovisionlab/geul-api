package page

import (
	"context"
	"crypto/sha256"
	"fmt"
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

// pageSortConfig defines allowed sort fields for pages
func (s *PageService) ListPageVersions(
	ctx context.Context,
	req *connect.Request[managev1.ListPageVersionsRequest],
) (*connect.Response[managev1.ListPageVersionsResponse], error) {
	if err := requirePagePermission(ctx, s.spiceDB, req.Msg.PageId, policyv1.Page.View); err != nil {
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
		Model(&model.PageVersion{}).
		Where("page_id = ?", req.Msg.PageId).
		Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	var versions []model.PageVersion
	if err := s.db.WithContext(ctx).
		Where("page_id = ?", req.Msg.PageId).
		Order("version DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&versions).Error; err != nil {
		return nil, errs.Internal(err)
	}

	protoVersions := make([]*managev1.PageVersion, len(versions))
	contributorLists := make([][]string, len(versions))
	for i := range versions {
		contributorLists[i] = versions[i].ContributorMemberIDs
	}
	contributorNames, err := resolveVersionContributorNames(ctx, s.db, contributorLists...)
	if err != nil {
		return nil, errs.Internal(err)
	}
	for i, v := range versions {
		protoVersion, err := s.toProtoPageVersion(&v, contributorNames)
		if err != nil {
			return nil, errs.Wrap(err)
		}
		protoVersions[i] = protoVersion
	}

	return connect.NewResponse(&managev1.ListPageVersionsResponse{
		Versions: protoVersions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// RestorePageVersion restores only the versioned source-locale title, summary,
// collaborative body, and shared document structure. Slug, visibility,
// document layout, and every other Page setting remain current.
func (s *PageService) RestorePageVersion(
	ctx context.Context,
	req *connect.Request[managev1.RestorePageVersionRequest],
) (*connect.Response[managev1.Page], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Page content Block store is not configured")
	}

	var version model.PageVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", req.Msg.VersionId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("page_version", req.Msg.VersionId)
		}
		return nil, errs.Internal(err)
	}

	// Validate version belongs to the specified page
	if req.Msg.PageId != "" && version.PageID != req.Msg.PageId {
		return nil, errs.NotFound("page_version", req.Msg.VersionId)
	}
	versionSnapshot, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, errs.FailedPrecondition("Page version typed snapshot is invalid")
	}

	var contributorMemberIDs []string
	if user := auth.GetUser(ctx); user != nil {
		contributorMemberIDs = append(contributorMemberIDs, user.MemberID.String())
	}
	now := time.Now().UTC()
	var restoredRevision string
	var blocksChanged bool
	var titleChanged bool
	var summaryChanged bool
	var sourceLocaleSwitched bool
	restoredChanged := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, documentID, err := loadPageAIDocumentRoot(ctx, tx, version.PageID, "UPDATE")
		if err != nil {
			return err
		}
		if err := requireLockedPagePermission(ctx, tx, s.spiceDB, version.PageID, policyv1.Page.Edit); err != nil {
			return err
		}
		currentSource, err := loadRequiredPageSourceLocaleMetadata(ctx, tx, version.PageID)
		if err != nil {
			return err
		}
		titleChanged = !nullableStringEqual(currentSource.Title, versionSnapshot.Title)
		summaryChanged = !nullableStringEqual(currentSource.Summary, versionSnapshot.Summary)

		currentSnapshot, err := s.contentBlocks.LoadSnapshotInTransaction(
			ctx,
			tx,
			documentID,
			root.SourceLocale,
		)
		if err != nil {
			return normalizePageContentBlockError(err)
		}
		if root.SourceLocale != versionSnapshot.SourceLocale {
			switchErr := switchBlockVersionRestoreSourceLocale(
				ctx, tx, s.contentBlocks, "page", version.PageID, versionSnapshot.SourceLocale,
				currentSnapshot.Document.Revision, root.SourceLocale, now,
			)
			if switchErr != nil {
				return switchErr
			}
			sourceLocaleSwitched = true
			root.SourceLocale = versionSnapshot.SourceLocale
			currentSource, err = loadRequiredPageSourceLocaleMetadata(ctx, tx, version.PageID)
			if err != nil {
				return err
			}
			titleChanged = !nullableStringEqual(currentSource.Title, versionSnapshot.Title)
			summaryChanged = !nullableStringEqual(currentSource.Summary, versionSnapshot.Summary)
			currentSnapshot, err = s.contentBlocks.LoadSnapshotInTransaction(
				ctx, tx, documentID, versionSnapshot.SourceLocale,
			)
			if err != nil {
				return normalizePageContentBlockError(err)
			}
		}
		unavailable, err := s.runtime.LoadUnavailableVersionAttachmentKinds(ctx, tx, versionSnapshot.Document)
		if err != nil {
			return normalizePageContentBlockError(err)
		}
		replaceInput, err := contentblock.ReplaceFromLocalizedPageProtoWithUnavailableAttachments(
			documentID,
			currentSnapshot.Document.Revision,
			versionSnapshot.Document,
			unavailable,
		)
		if err != nil {
			return normalizePageContentBlockError(err)
		}
		replaceResult, err := s.contentBlocks.ReplaceSnapshot(
			ctx,
			tx,
			replaceInput,
			lockedPageContentFence(version.PageID),
		)
		if err != nil {
			return normalizePageContentBlockError(err)
		}
		blocksChanged = replaceResult.Changed
		finalRevision := replaceResult.DocumentRevision

		metadataChanged := titleChanged || summaryChanged
		if metadataChanged && !replaceResult.Changed {
			advanceResult, err := s.contentBlocks.AdvanceRevision(
				ctx,
				tx,
				contentblock.AdvanceInput{
					DocumentID:       documentID,
					ExpectedRevision: finalRevision,
				},
				lockedPageContentFence(version.PageID),
				func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
					if err := updatePageVersionSourceMetadata(
						ctx,
						tx,
						version.PageID,
						versionSnapshot.SourceLocale,
						versionSnapshot.Title,
						versionSnapshot.Summary,
						now,
					); err != nil {
						return contentblock.MetadataEffect{}, err
					}
					return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
				},
			)
			if err != nil {
				return normalizePageContentBlockError(err)
			}
			finalRevision = advanceResult.DocumentRevision
		} else if metadataChanged {
			if err := updatePageVersionSourceMetadata(
				ctx,
				tx,
				version.PageID,
				versionSnapshot.SourceLocale,
				versionSnapshot.Title,
				versionSnapshot.Summary,
				now,
			); err != nil {
				return err
			}
		}
		restoredChanged = sourceLocaleSwitched || blocksChanged || metadataChanged
		restoredRevision = finalRevision.String()
		if !restoredChanged {
			return nil
		}

		if err := tx.WithContext(ctx).Model(&model.Page{}).
			Where("id = ?", version.PageID).
			Updates(structured.Fields{
				"updated_at": now,
			}).Error; err != nil {
			return err
		}

		_, err = s.runtime.RequestCurrentWithDB(
			ctx,
			tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
			version.PageID,
			versionSnapshot.SourceLocale,
			false,
			"page_version_restored",
		)
		if err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditPageUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageVersionRestoreAuditRecord(metadata, version.PageID, version.ID)
			},
		)
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	if restoredChanged {
		changedFields := make([]string, 0, 3)
		if titleChanged {
			changedFields = append(changedFields, "title")
		}
		if summaryChanged {
			changedFields = append(changedFields, "summary")
		}
		if blocksChanged {
			changedFields = append(changedFields, "content")
		}
		if sourceLocaleSwitched {
			changedFields = append(changedFields, "sourceLocale")
		}
		event := buildContentUpdatedEvent(
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
			version.PageID,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
			buildContentUpdatedFields(changedFields, pageContentUpdatedFieldSpecs),
			true,
			restoredRevision,
			contributorMemberIDs,
			&contentUpdatedLocaleState{locale: versionSnapshot.SourceLocale, exists: true},
		)
		publishContentUpdatedEvent(ctx, s.asyncPublisher, event)
	}
	var page model.Page
	if err := s.db.WithContext(ctx).First(&page, "id = ?", version.PageID).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return s.pageResponseWithReadyOg(ctx, &page)
}

func updatePageVersionSourceMetadata(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	sourceLocale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	result := tx.WithContext(ctx).
		Table("page_translation").
		Where("entity_id = ? AND locale = ?", pageID, sourceLocale).
		Updates(structured.Fields{
			"title":      title,
			"summary":    summary,
			"updated_at": now,
		})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("Page source locale changed during version restore")
	}
	return nil
}

func (s *PageService) toProtoPageVersion(
	v *model.PageVersion,
	contributorNames map[string]string,
) (*managev1.PageVersion, error) {
	snapshot, err := DecodeVersionContentSnapshot(v.ContentSnapshot)
	if err != nil {
		return nil, errs.FailedPrecondition("Page version typed snapshot is invalid")
	}
	contributors, err := toProtoVersionContributors(v.ContributorMemberIDs, contributorNames)
	if err != nil {
		return nil, err
	}
	pv := &managev1.PageVersion{
		Id:            v.ID,
		Version:       v.Version,
		SourceLocale:  snapshot.SourceLocale,
		Title:         v.Title,
		CanonicalHash: fmt.Sprintf("%x", sha256.Sum256(v.ContentSnapshot)),
		CreatedAt:     timestamppb.New(v.CreatedAt),
		Contributors:  contributors,
	}
	if v.Summary != nil {
		pv.Summary = v.Summary
	}
	return pv, nil
}
