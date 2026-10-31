package page

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ListPageVersionsQuery struct {
	PageID string
	Page   Page
}

// RestorePageVersionCommand restores VersionID. A non-empty PageID must own
// the version.
type RestorePageVersionCommand struct {
	PageID    string
	VersionID string
}

// ListPageVersions returns a page of the version checkpoints of a Page the
// caller may view.
func (s *Service) ListPageVersions(ctx context.Context, query ListPageVersionsQuery) (_ *managev1.ListPageVersionsResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.PageID))

	if err := requirePagePermission(ctx, s.permissions, query.PageID, policyv1.Page.View); err != nil {
		return nil, err
	}
	limit, offset := query.Page.limitOr(20), query.Page.Offset

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.PageVersion{}).Where("page_id = ?", query.PageID).Count(&total).Error; err != nil {
		return nil, err
	}
	var versions []model.PageVersion
	if err := s.db.WithContext(ctx).
		Where("page_id = ?", query.PageID).
		Order("version DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&versions).Error; err != nil {
		return nil, err
	}

	contributorLists := make([][]string, len(versions))
	for i := range versions {
		contributorLists[i] = versions[i].ContributorMemberIDs
	}
	contributorNames, err := resolveVersionContributorNames(ctx, s.db, contributorLists...)
	if err != nil {
		return nil, err
	}
	protoVersions := make([]*managev1.PageVersion, len(versions))
	for i := range versions {
		if protoVersions[i], err = toProtoPageVersion(&versions[i], contributorNames); err != nil {
			return nil, err
		}
	}
	return &managev1.ListPageVersionsResponse{
		Versions: protoVersions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: int64(offset)+int64(limit) < total,
		},
	}, nil
}

// RestorePageVersion restores only the versioned source-locale title, summary,
// collaborative body, and shared document structure. Slug, visibility,
// document layout, and every other Page setting remain current.
func (s *Service) RestorePageVersion(ctx context.Context, command RestorePageVersionCommand) (_ *managev1.Page, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID), slog.String("version_id", command.VersionID))

	var version model.PageVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", command.VersionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("page_version", command.VersionID)
		}
		return nil, err
	}
	if command.PageID != "" && version.PageID != command.PageID {
		return nil, domainerr.ResourceNotFound("page_version", command.VersionID)
	}
	snapshot, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, failurecause.WithAttrs(
			domainerr.Precondition("Page version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
			slog.String("page_version_id", version.ID),
		)
	}

	now := time.Now().UTC()
	var documentID uuid.UUID
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[versionrestore.Outcome]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			var err error
			if _, documentID, err = loadPageAIDocumentRoot(ctx, tx, version.PageID, clause.LockingStrengthUpdate); err != nil {
				return err
			}
			return requireLockedPagePermission(ctx, tx, s.permissions, version.PageID, policyv1.Page.Edit)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (versionrestore.Outcome, error) {
			outcome, err := s.pageVersionRestore(version.PageID, documentID, snapshot, now).Apply(ctx, tx)
			if err != nil || !outcome.Changed() {
				return outcome, err
			}
			return outcome, s.touchRestoredPage(ctx, tx, version.PageID, snapshot.SourceLocale, outcome, now)
		},
		Audit: func(outcome versionrestore.Outcome) mutation.Audit {
			if !outcome.Changed() {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageVersionRestoreAuditRecord(metadata, version.PageID, version.ID)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, outcome versionrestore.Outcome) error {
			if !outcome.Changed() {
				return nil
			}
			return s.publish(ctx, tx, buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID: version.PageID, Fields: outcome.Fields(),
				DocumentRevision: outcome.DocumentRevision.String(), Contributors: versionrestore.Contributors(ctx),
				Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, Locale: snapshot.SourceLocale,
				DocumentStateChanged: true,
			}))
		},
	}); err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	return s.pageBy(ctx, "id", version.PageID)
}

// pageVersionRestore is the restore of snapshot into the locked Page.
func (s *Service) pageVersionRestore(
	pageID string,
	documentID uuid.UUID,
	snapshot VersionContentSnapshot,
	now time.Time,
) versionrestore.Restore {
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
	}
}

// touchRestoredPage records a restore that changed the Page on its root and
// requests the OG image of a changed title.
func (s *Service) touchRestoredPage(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	sourceLocale string,
	outcome versionrestore.Outcome,
	now time.Time,
) error {
	if err := tx.WithContext(ctx).Model(&model.Page{}).
		Where("id = ?", pageID).
		UpdateColumn("updated_at", now).Error; err != nil {
		return err
	}
	if !outcome.RefreshesOG() {
		return nil
	}
	_, err := s.runtime.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, pageID, sourceLocale, false, "page_version_restored",
	)
	return err
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
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domainerr.Precondition("Page source locale changed during version restore")
	}
	return nil
}

func toProtoPageVersion(
	v *model.PageVersion,
	contributorNames map[string]string,
) (*managev1.PageVersion, error) {
	snapshot, err := DecodeVersionContentSnapshot(v.ContentSnapshot)
	if err != nil {
		return nil, failurecause.WithAttrs(
			domainerr.Precondition("Page version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
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
