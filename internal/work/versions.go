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

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
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
			return nil, errs.WithCause(errs.FailedPrecondition("Work Version typed snapshot is invalid"), failurecause.ServerFault(err))
		}
	}

	return connect.NewResponse(&managev1.ListWorkVersionsResponse{
		Versions: protoVersions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: int64(offset)+int64(limit) < total,
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
	envelope, document, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, errs.WithCause(errs.FailedPrecondition("Work Version typed snapshot is invalid"), failurecause.ServerFault(err))
	}
	if envelope.Title == nil || strings.TrimSpace(*envelope.Title) == "" {
		return nil, errs.FailedPrecondition("Work Version source title is invalid")
	}

	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		restore, lockErr := s.lockWorkVersionRestore(ctx, tx, version.WorkID, envelope, document, now)
		if lockErr != nil {
			return lockErr
		}
		outcome, restoreErr := restore.Apply(ctx, tx)
		if restoreErr != nil || !outcome.Changed() {
			return restoreErr
		}
		return s.publishWorkVersionRestore(ctx, tx, version, envelope.SourceLocale, outcome, now)
	})
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}

	work, err := s.loadRestoredWork(ctx, version.WorkID)
	if err != nil {
		return nil, err
	}
	return s.workResponseWithReadyOg(ctx, &work, s.getWorkFeaturedImageAsset(ctx, work.ID))
}

// lockWorkVersionRestore locks the Work root, authorizes the caller and
// returns the restore of envelope and document.
func (s *WorkService) lockWorkVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
	envelope VersionContentSnapshot,
	document *contentv1.LocalizedRichTextDocument,
	now time.Time,
) (versionrestore.Restore, error) {
	documentID, err := loadWorkContentDocumentID(ctx, tx, workID)
	if err != nil {
		return versionrestore.Restore{}, err
	}
	work, err := lockWorkForContentDocument(ctx, tx, documentID)
	if err != nil {
		return versionrestore.Restore{}, err
	}
	if work.ID != workID {
		return versionrestore.Restore{}, errs.FailedPrecondition("Work content document changed during version restore")
	}
	if _, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, workID, policyv1.Work.Edit, workAuthorizationMutation); err != nil {
		return versionrestore.Restore{}, err
	}
	return versionrestore.Restore{
		Store: s.contentBlocks, DocumentID: documentID,
		Version: versionrestore.Source{Locale: envelope.SourceLocale, Title: envelope.Title, Summary: envelope.Summary},
		Fence:   lockedWorkContentFence(),
		LoadSource: func(ctx context.Context, tx *gorm.DB) (versionrestore.Source, error) {
			source, err := LoadRequiredSourceLocaleMetadata(ctx, tx, workID)
			if err != nil {
				return versionrestore.Source{}, err
			}
			return versionrestore.Source{Locale: source.Locale, Title: source.Title, Summary: source.Summary}, nil
		},
		SwitchSourceLocale: func(ctx context.Context, tx *gorm.DB, from string, revision uuid.UUID) error {
			return switchWorkVersionRestoreSourceLocale(
				ctx, tx, s.contentBlocks, workID, envelope.SourceLocale, revision, from, now,
			)
		},
		Replacement: func(ctx context.Context, tx *gorm.DB, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			unavailable, err := s.runtime.LoadUnavailableVersionAttachmentKinds(ctx, tx, document)
			if err != nil {
				return contentblock.ReplaceInput{}, err
			}
			return contentblock.ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
				documentID, revision, document, unavailable,
			)
		},
		UpdateMetadata: func(ctx context.Context, tx *gorm.DB) error {
			return updateWorkVersionSourceMetadata(
				ctx, tx, workID, envelope.SourceLocale, envelope.Title, envelope.Summary, now,
			)
		},
	}, nil
}

// publishWorkVersionRestore records a restore that changed the Work.
func (s *WorkService) publishWorkVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	version model.WorkVersion,
	sourceLocale string,
	outcome versionrestore.Outcome,
	now time.Time,
) error {
	if err := tx.WithContext(ctx).Model(&model.Work{}).
		Where("id = ?", version.WorkID).
		UpdateColumn("updated_at", now).Error; err != nil {
		return err
	}
	if outcome.RefreshesOG() {
		if _, err := s.runtime.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
			version.WorkID, sourceLocale, false, "work_version_restored",
		); err != nil {
			return err
		}
	}
	if s.auditWriter != nil {
		if err := domainaudit.AppendRequest(
			ctx, tx, s.auditWriter, sharedtelemetry.AuditWorkUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkVersionRestoreAuditRecord(metadata, version.WorkID, version.ID)
			},
		); err != nil {
			return err
		}
	}
	return publishWorkContentUpdated(ctx, tx, s.asyncPublisher, buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID: version.WorkID, Fields: outcome.Fields(),
		DocumentRevision: outcome.DocumentRevision.String(), Contributors: versionrestore.Contributors(ctx),
		Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, Locale: sourceLocale,
		DocumentStateChanged: true,
	}))
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
	sourceState, err := loadWorkSourceLocaleDocumentState(ctx, s.db, work.ID)
	if err != nil {
		return model.Work{}, err
	}
	if sourceState != nil {
		work.OgAssetID = sourceState.OgAssetID
	}
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
