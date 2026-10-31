package post

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
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
		return domainerr.InvalidField("source_locale", "source-locale switch identity is invalid")
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
				return contentblock.MetadataEffect{}, domainerr.Precondition("Post source locale changed; reload before restoring")
			}
			return contentblock.MetadataEffect{
				Changed: true, AffectsTranslationSource: true, SourceLocale: nextLocale,
				ChangedLocales: []string{nextLocale},
			}, nil
		},
	)
	return err
}

// ListPostVersionsQuery lists a Post's version checkpoints, newest first.
type ListPostVersionsQuery struct {
	PostID string
	Page   Page
}

// RestorePostVersionCommand restores a Post's source content to a version.
type RestorePostVersionCommand struct{ PostID, VersionID string }

func (s *Service) ListPostVersions(ctx context.Context, query ListPostVersionsQuery) (_ *managev1.ListPostVersionsResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	limit, offset := query.Page.limitOr(20), query.Page.Offset
	var total int64
	var versions []model.PostVersion
	access := mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		var root struct {
			Status model.PostStatus `gorm:"column:status"`
		}
		if err := tx.WithContext(ctx).Table("post").Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Select("status").Where("id = ?::uuid", query.PostID).Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.ResourceNotFound("post", query.PostID)
			}
			return err
		}
		_, err := requirePostViewForStatus(ctx, s.spiceDB, query.PostID, root.Status)
		return err
	})
	if err := s.readLocked(ctx, access, func(_ context.Context, tx *gorm.DB) error {
		if err := tx.Model(&model.PostVersion{}).Where("post_id = ?", query.PostID).Count(&total).Error; err != nil {
			return err
		}
		return tx.Where("post_id = ?", query.PostID).Order("version DESC").
			Limit(int(limit)).Offset(int(offset)).Find(&versions).Error
	}); err != nil {
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
	result := make([]*managev1.PostVersion, len(versions))
	for i := range versions {
		result[i], err = s.toProtoPostVersion(&versions[i], contributorNames)
		if err != nil {
			return nil, failurecause.WithAttrs(
				domainerr.Precondition("Post Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
				slog.String("post_version_id", versions[i].ID),
			)
		}
	}
	return &managev1.ListPostVersionsResponse{
		Versions: result,
		Pagination: &commonv1.PaginationResponse{
			Total: int32(total), Limit: limit, Offset: offset, HasMore: int64(offset)+int64(limit) < total,
		},
	}, nil
}

// RestorePostVersion replaces the full source-owned graph and source overlay.
// A snapshot locale change switches source authority without rewriting stored
// locale rows or already requested translation jobs.
// Category and Tag remain current document metadata and are not versioned.
func (s *Service) RestorePostVersion(ctx context.Context, command RestorePostVersionCommand) (_ *managev1.Post, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID), slog.String("version_id", command.VersionID))

	var version model.PostVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", command.VersionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("post_version", command.VersionID)
		}
		return nil, err
	}
	if command.PostID != "" && version.PostID != command.PostID {
		return nil, domainerr.ResourceNotFound("post_version", command.VersionID)
	}
	envelope, document, err := unmarshalPostVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, failurecause.WithAttrs(
			domainerr.Precondition("Post Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err)),
			slog.String("post_version_id", version.ID),
		)
	}
	now := time.Now().UTC()
	var restore versionrestore.Restore
	_, _, err = runPostMutation(ctx, s, mutation.Spec[struct{}]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			var err error
			restore, err = s.lockPostVersionRestore(ctx, tx, require, version.PostID, envelope, document, now)
			return err
		}),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (struct{}, error) {
		outcome, err := restore.Apply(ctx, tx)
		if err != nil || !outcome.Changed() {
			return struct{}{}, err
		}
		return struct{}{}, s.recordPostVersionRestore(ctx, tx, effects, version, envelope.SourceLocale, outcome, now)
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	var post model.Post
	if err := s.db.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		First(&post, "id = ?", version.PostID).Error; err != nil {
		return nil, err
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &post); err != nil {
		return nil, err
	}
	return s.postResponseWithReadyOg(ctx, &post)
}

// lockPostVersionRestore locks the Post root, authorizes the caller and
// returns the restore of envelope and document.
func (s *Service) lockPostVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	require mutation.Require,
	postID string,
	envelope postVersionContentSnapshot,
	document *contentv1.LocalizedRichTextDocument,
	now time.Time,
) (versionrestore.Restore, error) {
	documentID, err := loadPostContentDocumentID(ctx, tx, postID)
	if err != nil {
		return versionrestore.Restore{}, err
	}
	root, err := lockPostContentDocumentRoot(ctx, tx, postID, documentID, true)
	if err != nil {
		return versionrestore.Restore{}, err
	}
	if _, err := requireLockedPostActionForStatus(require, postID, root.Status, policyv1.Post.Manage); err != nil {
		return versionrestore.Restore{}, err
	}
	return versionrestore.Restore{
		Store: s.contentBlocks, DocumentID: documentID,
		Version: versionrestore.Source{Locale: envelope.SourceLocale, Title: envelope.Title, Summary: envelope.Summary},
		Fence:   postLockedDocumentFence(postID, true),
		LoadSource: func(ctx context.Context, tx *gorm.DB) (versionrestore.Source, error) {
			domain, err := loadPostContentDomainContext(ctx, tx, postID)
			if err != nil {
				return versionrestore.Source{}, err
			}
			metadata, err := loadPostLocaleMetadataRow(ctx, tx, postID, domain.SourceLocale)
			if err != nil {
				return versionrestore.Source{}, err
			}
			return versionrestore.Source{Locale: domain.SourceLocale, Title: metadata.Title, Summary: metadata.Summary}, nil
		},
		SwitchSourceLocale: func(ctx context.Context, tx *gorm.DB, from string, revision uuid.UUID) error {
			return switchPostVersionRestoreSourceLocale(
				ctx, tx, s.contentBlocks, postID, documentID, from, envelope.SourceLocale, revision, now,
			)
		},
		Replacement: func(ctx context.Context, tx *gorm.DB, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			unavailable, err := s.versionRestore.LoadUnavailableVersionAttachmentKinds(ctx, tx, document)
			if err != nil {
				return contentblock.ReplaceInput{}, err
			}
			return contentblock.ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
				documentID, revision, document, unavailable,
			)
		},
		UpdateMetadata: func(ctx context.Context, tx *gorm.DB) error {
			return updatePostVersionSourceMetadata(
				ctx, tx, postID, envelope.SourceLocale, envelope.Title, envelope.Summary, now,
			)
		},
	}, nil
}

// recordPostVersionRestore touches a Post a restore changed and names its
// record, OG request and signal.
func (s *Service) recordPostVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	effects *postEffects,
	version model.PostVersion,
	sourceLocale string,
	outcome versionrestore.Outcome,
	now time.Time,
) error {
	if err := tx.WithContext(ctx).Model(&model.Post{}).
		Where("id = ?", version.PostID).
		UpdateColumn("updated_at", now).Error; err != nil {
		return err
	}
	if outcome.RefreshesOG() {
		effects.requestOG(version.PostID, sourceLocale, false, "post_version_restored")
	}
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostVersionRestoreAuditRecord(metadata, version.PostID, version.ID)
	})
	effects.signal(buildPostLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID: version.PostID, Fields: outcome.Fields(),
		DocumentRevision: outcome.DocumentRevision.String(), Contributors: versionrestore.Contributors(ctx),
		Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, Locale: sourceLocale,
		DocumentStateChanged: true,
	}))
	return nil
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
		return domainerr.Precondition("Post source locale metadata is not initialized")
	}
	return nil
}

func (s *Service) toProtoPostVersion(
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
