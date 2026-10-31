package page

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// pageSortConfig defines allowed sort fields for pages
func (s *PageService) ListPageVersions(
	ctx context.Context,
	req *connect.Request[managev1.ListPageVersionsRequest],
) (_ *connect.Response[managev1.ListPageVersionsResponse], returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", req.Msg.GetPageId()))

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
			HasMore: int64(offset)+int64(limit) < total,
		},
	}), nil
}

// RestorePageVersion restores only the versioned source-locale title, summary,
// collaborative body, and shared document structure. Slug, visibility,
// document layout, and every other Page setting remain current.
func (s *PageService) RestorePageVersion(
	ctx context.Context,
	req *connect.Request[managev1.RestorePageVersionRequest],
) (_ *connect.Response[managev1.Page], returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", req.Msg.GetPageId()), slog.String("version_id", req.Msg.GetVersionId()))

	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Page content Block store is not configured")
	}

	var version model.PageVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", req.Msg.VersionId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("page_version", req.Msg.VersionId)
		}
		return nil, errs.Internal(err)
	}
	if req.Msg.PageId != "" && version.PageID != req.Msg.PageId {
		return nil, errs.NotFound("page_version", req.Msg.VersionId)
	}
	snapshot, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, failurecause.WithAttrs(
			errs.WithCause(errs.FailedPrecondition("Page version typed snapshot is invalid"), failurecause.ServerFault(err)),
			slog.String("page_version_id", version.ID),
		)
	}

	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		restore, err := s.lockPageVersionRestore(ctx, tx, version.PageID, snapshot, now)
		if err != nil {
			return err
		}
		outcome, err := restore.Apply(ctx, tx)
		if err != nil || !outcome.Changed() {
			return err
		}
		return s.publishPageVersionRestore(ctx, tx, version, snapshot.SourceLocale, outcome, now)
	}); err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	var page model.Page
	if err := s.db.WithContext(ctx).First(&page, "id = ?", version.PageID).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return s.pageResponseWithReadyOg(ctx, &page)
}

// lockPageVersionRestore locks the Page root, authorizes the caller and
// returns the restore of snapshot.
func (s *PageService) lockPageVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	snapshot VersionContentSnapshot,
	now time.Time,
) (versionrestore.Restore, error) {
	_, documentID, err := loadPageAIDocumentRoot(ctx, tx, pageID, clause.LockingStrengthUpdate)
	if err != nil {
		return versionrestore.Restore{}, err
	}
	if err := requireLockedPagePermission(ctx, tx, s.spiceDB, pageID, policyv1.Page.Edit); err != nil {
		return versionrestore.Restore{}, err
	}
	return versionrestore.Restore{
		Store: s.contentBlocks, DocumentID: documentID,
		Version: versionrestore.Source{Locale: snapshot.SourceLocale, Title: snapshot.Title, Summary: snapshot.Summary},
		Fence:   lockedPageContentFence(pageID),
		LoadSource: func(ctx context.Context, tx *gorm.DB) (versionrestore.Source, error) {
			source, err := loadRequiredPageSourceLocaleMetadata(ctx, tx, pageID)
			if err != nil {
				return versionrestore.Source{}, err
			}
			return versionrestore.Source{Locale: source.Locale, Title: source.Title, Summary: source.Summary}, nil
		},
		SwitchSourceLocale: func(ctx context.Context, tx *gorm.DB, from string, revision uuid.UUID) error {
			return switchBlockVersionRestoreSourceLocale(
				ctx, tx, s.contentBlocks, "page", pageID, snapshot.SourceLocale, revision, from, now,
			)
		},
		Replacement: func(ctx context.Context, tx *gorm.DB, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			unavailable, err := s.runtime.LoadUnavailableVersionAttachmentKinds(ctx, tx, snapshot.Document)
			if err != nil {
				return contentblock.ReplaceInput{}, err
			}
			return contentblock.ReplaceFromLocalizedPageProtoWithUnavailableAttachments(
				documentID, revision, snapshot.Document, unavailable,
			)
		},
		UpdateMetadata: func(ctx context.Context, tx *gorm.DB) error {
			return updatePageVersionSourceMetadata(
				ctx, tx, pageID, snapshot.SourceLocale, snapshot.Title, snapshot.Summary, now,
			)
		},
	}, nil
}

// publishPageVersionRestore records a restore that changed the Page.
func (s *PageService) publishPageVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	version model.PageVersion,
	sourceLocale string,
	outcome versionrestore.Outcome,
	now time.Time,
) error {
	if err := tx.WithContext(ctx).Model(&model.Page{}).
		Where("id = ?", version.PageID).
		UpdateColumn("updated_at", now).Error; err != nil {
		return err
	}
	if outcome.RefreshesOG() {
		if _, err := s.runtime.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
			version.PageID, sourceLocale, false, "page_version_restored",
		); err != nil {
			return err
		}
	}
	if s.auditWriter != nil {
		if err := domainaudit.AppendRequest(
			ctx, tx, s.auditWriter, sharedtelemetry.AuditPageUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageVersionRestoreAuditRecord(metadata, version.PageID, version.ID)
			},
		); err != nil {
			return err
		}
	}
	return publishContentUpdatedEvent(ctx, tx, s.asyncPublisher, buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID: version.PageID, Fields: outcome.Fields(),
		DocumentRevision: outcome.DocumentRevision.String(), Contributors: versionrestore.Contributors(ctx),
		Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, Locale: sourceLocale,
		DocumentStateChanged: true,
	}))
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
		return nil, failurecause.WithAttrs(
			errs.WithCause(errs.FailedPrecondition("Page version typed snapshot is invalid"), failurecause.ServerFault(err)),
			slog.String("page_version_id", v.ID),
		)
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
