package post

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// switchPostVersionRestoreSourceLocale moves source-locale authority and
// advances the shared document CAS in the same transaction. The post root is
// already locked by RestorePostVersion, so the callback only rechecks the
// expected old locale before changing the owning row.
func switchPostVersionRestoreSourceLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	postID string,
	documentID uuid.UUID,
	previousLocale string,
	nextLocale string,
	expectedRevision uuid.UUID,
	now time.Time,
) error {
	if strings.TrimSpace(previousLocale) == "" || strings.TrimSpace(nextLocale) == "" ||
		previousLocale == nextLocale {
		return errs.InvalidArgument("source_locale", "source-locale switch identity is invalid")
	}
	_, err := store.AdvanceRevision(
		ctx,
		tx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: previousLocale}, nil
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			updated := tx.WithContext(ctx).Table("post").
				Where("id = ?::uuid AND source_locale = ?", postID, previousLocale).
				Updates(map[string]any{"source_locale": nextLocale, "updated_at": now})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, updated.Error
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.FailedPrecondition("Post source locale changed; reload before restoring")
			}
			return contentblock.MetadataEffect{
				Changed: true, AffectsTranslationSource: true, SourceLocale: nextLocale,
				ChangedLocales: []string{nextLocale},
			}, nil
		},
	)
	return err
}

func (s *PostService) ListPostVersions(
	ctx context.Context,
	req *connect.Request[managev1.ListPostVersionsRequest],
) (*connect.Response[managev1.ListPostVersionsResponse], error) {
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}
	var total int64
	var versions []model.PostVersion
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root struct {
			Status model.PostStatus `gorm:"column:status"`
		}
		if err := tx.WithContext(ctx).Table("post").Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Select("status").Where("id = ?::uuid", req.Msg.PostId).Take(&root).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.PostId)
			}
			return errs.Internal(err)
		}
		if _, err := requirePostViewForStatus(ctx, s.spiceDB, req.Msg.PostId, root.Status); err != nil {
			return err
		}
		if err := tx.Model(&model.PostVersion{}).Where("post_id = ?", req.Msg.PostId).Count(&total).Error; err != nil {
			return errs.Internal(err)
		}
		if err := tx.Where("post_id = ?", req.Msg.PostId).Order("version DESC").
			Limit(int(limit)).Offset(int(offset)).Find(&versions).Error; err != nil {
			return errs.Internal(err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	contributorLists := make([][]string, len(versions))
	for i := range versions {
		contributorLists[i] = versions[i].ContributorMemberIDs
	}
	contributorNames, err := resolveVersionContributorNames(ctx, s.db, contributorLists...)
	if err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*managev1.PostVersion, len(versions))
	for i := range versions {
		result[i], err = s.toProtoPostVersion(&versions[i], contributorNames)
		if err != nil {
			return nil, errs.FailedPrecondition("Post Version typed snapshot is invalid")
		}
	}
	return connect.NewResponse(&managev1.ListPostVersionsResponse{
		Versions: result,
		Pagination: &commonv1.PaginationResponse{
			Total: int32(total), Limit: limit, Offset: offset, HasMore: offset+limit < int32(total),
		},
	}), nil
}

// RestorePostVersion replaces the full source-owned graph and source overlay.
// A snapshot locale change switches source authority without rewriting stored
// locale rows or already requested translation jobs.
// Category and Tag remain current document metadata and are not versioned.
func (s *PostService) RestorePostVersion(
	ctx context.Context,
	req *connect.Request[managev1.RestorePostVersionRequest],
) (*connect.Response[managev1.Post], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Post content Block store is not configured")
	}
	var version model.PostVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", req.Msg.VersionId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("post_version", req.Msg.VersionId)
		}
		return nil, errs.Internal(err)
	}
	if req.Msg.PostId != "" && version.PostID != req.Msg.PostId {
		return nil, errs.NotFound("post_version", req.Msg.VersionId)
	}
	versionEnvelope, versionDocument, err := unmarshalPostVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, errs.FailedPrecondition("Post Version typed snapshot is invalid")
	}

	now := time.Now().UTC()
	var finalSnapshot contentblock.Snapshot
	var changed bool
	var sourceLocaleSwitched bool
	var titleChanged bool
	var summaryChanged bool
	var blocksChanged bool
	var restoreContributors []string
	if user := auth.GetUser(ctx); user != nil {
		restoreContributors = []string{user.MemberID.String()}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, loadErr := loadPostContentDocumentID(ctx, tx, version.PostID)
		if loadErr != nil {
			return loadErr
		}
		root, lockErr := lockPostContentDocumentRoot(ctx, tx, version.PostID, documentID, true)
		if lockErr != nil {
			return lockErr
		}
		if _, permissionErr := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, version.PostID, root.Status, policyv1.Post.Manage); permissionErr != nil {
			return permissionErr
		}
		domain, domainErr := loadPostContentDomainContext(ctx, tx, version.PostID)
		if domainErr != nil {
			return domainErr
		}
		if domain.SourceLocale != versionEnvelope.SourceLocale {
			currentSnapshot, snapshotErr := s.contentBlocks.LoadSnapshotInTransaction(
				ctx, tx, documentID, domain.SourceLocale,
			)
			if snapshotErr != nil {
				return snapshotErr
			}
			switchErr := switchPostVersionRestoreSourceLocale(
				ctx, tx, s.contentBlocks, version.PostID, documentID, domain.SourceLocale,
				versionEnvelope.SourceLocale, currentSnapshot.Document.Revision, now,
			)
			if switchErr != nil {
				return switchErr
			}
			sourceLocaleSwitched = true
			domain.SourceLocale = versionEnvelope.SourceLocale
		}
		currentMetadata, metadataErr := loadPostLocaleMetadataRow(
			ctx, tx, version.PostID, domain.SourceLocale,
		)
		if metadataErr != nil {
			return metadataErr
		}
		titleChanged = !nullableStringEqual(currentMetadata.Title, versionEnvelope.Title)
		summaryChanged = !nullableStringEqual(currentMetadata.Summary, versionEnvelope.Summary)

		currentSnapshot, snapshotErr := s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, domain.SourceLocale,
		)
		if snapshotErr != nil {
			return snapshotErr
		}
		unavailable, unavailableErr := s.versionRestore.LoadUnavailableVersionAttachmentKinds(ctx, tx, versionDocument)
		if unavailableErr != nil {
			return unavailableErr
		}
		replaceInput, replaceErr := contentblock.ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
			documentID, currentSnapshot.Document.Revision, versionDocument, unavailable,
		)
		if replaceErr != nil {
			return replaceErr
		}
		replaceResult, replaceErr := s.contentBlocks.ReplaceSnapshot(
			ctx,
			tx,
			replaceInput,
			postLockedDocumentFence(version.PostID, true),
		)
		if replaceErr != nil {
			return replaceErr
		}
		finalRevision := replaceResult.DocumentRevision
		metadataChanged := titleChanged || summaryChanged
		if metadataChanged && !replaceResult.Changed {
			_, advanceErr := s.contentBlocks.AdvanceRevision(
				ctx,
				tx,
				contentblock.AdvanceInput{
					DocumentID: documentID, ExpectedRevision: finalRevision,
				},
				postLockedDocumentFence(version.PostID, true),
				func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
					if err := updatePostVersionSourceMetadata(
						ctx,
						tx,
						version.PostID,
						domain.SourceLocale,
						versionEnvelope.Title,
						versionEnvelope.Summary,
						now,
					); err != nil {
						return contentblock.MetadataEffect{}, err
					}
					return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
				},
			)
			if advanceErr != nil {
				return advanceErr
			}
		} else if metadataChanged {
			if err := updatePostVersionSourceMetadata(
				ctx,
				tx,
				version.PostID,
				domain.SourceLocale,
				versionEnvelope.Title,
				versionEnvelope.Summary,
				now,
			); err != nil {
				return err
			}
		}
		blocksChanged = replaceResult.Changed
		changed = sourceLocaleSwitched || blocksChanged || metadataChanged
		if !changed {
			return nil
		}
		var loadFinalErr error
		finalSnapshot, loadFinalErr = s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, domain.SourceLocale,
		)
		if loadFinalErr != nil {
			return loadFinalErr
		}
		if err := tx.WithContext(ctx).Model(&model.Post{}).
			Where("id = ?", version.PostID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return err
		}
		if titleChanged || sourceLocaleSwitched {
			if _, err := s.ogRefresher.RequestCurrentWithDB(
				ctx,
				tx,
				managev1.OgEntityType_OG_ENTITY_TYPE_POST,
				version.PostID,
				domain.SourceLocale,
				false,
				"post_version_restored",
			); err != nil {
				return err
			}
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditPostUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPostVersionRestoreAuditRecord(metadata, version.PostID, version.ID)
			},
		)
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	if changed {
		fields := make([]string, 0, 4)
		if blocksChanged {
			fields = append(fields, "content")
		}
		if titleChanged {
			fields = append(fields, "title")
		}
		if summaryChanged {
			fields = append(fields, "summary")
		}
		if sourceLocaleSwitched {
			fields = append(fields, "sourceLocale")
		}
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, buildPostBlockContentUpdatedEvent(
			version.PostID,
			fields,
			finalSnapshot.Document.Revision.String(),
			restoreContributors,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
			finalSnapshot.SourceLocale,
			true,
			nil,
			true,
		))
	}
	var post model.Post
	if err := s.db.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		First(&post, "id = ?", version.PostID).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &post); err != nil {
		return nil, err
	}
	return s.postResponseWithReadyOg(ctx, &post)
}

func updatePostVersionSourceMetadata(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	locale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	result := tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Updates(structured.Fields{
			"title": title, "summary": summary,
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("Post source locale metadata is not initialized")
	}
	return nil
}

func (s *PostService) toProtoPostVersion(
	version *model.PostVersion,
	contributorNames map[string]string,
) (*managev1.PostVersion, error) {
	if version == nil {
		return nil, fmt.Errorf("post Version is required")
	}
	envelope, _, err := unmarshalPostVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, err
	}
	contributors, err := toProtoVersionContributors(version.ContributorMemberIDs, contributorNames)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(version.ContentSnapshot)
	result := &managev1.PostVersion{
		Id: version.ID, Version: version.Version,
		SourceLocale:  envelope.SourceLocale,
		CanonicalHash: fmt.Sprintf("%x", digest[:]),
		CreatedAt:     timestamppb.New(version.CreatedAt), Contributors: contributors,
	}
	if envelope.Title != nil {
		result.Title = *envelope.Title
	}
	result.Summary = cloneNullableString(envelope.Summary)
	return result, nil
}
