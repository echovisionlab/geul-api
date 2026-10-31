package work

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

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

// ListWorkVersionsQuery lists a Work's version checkpoints, newest first.
type ListWorkVersionsQuery struct {
	WorkID string
	Page   Page
}

// RestoreWorkVersionCommand restores a Work's source content to a version.
type RestoreWorkVersionCommand struct{ WorkID, VersionID string }

func (s *Service) ListWorkVersions(ctx context.Context, query ListWorkVersionsQuery) (*managev1.ListWorkVersionsResponse, error) {
	var root model.Work
	if err := s.db.WithContext(ctx).Select("id", "status").Take(&root, "id = ?", query.WorkID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("work", query.WorkID)
		}
		return nil, err
	}
	if err := requireWorkPermission(ctx, s.spiceDB, root, policyv1.Work.View, workAuthorizationRead); err != nil {
		return nil, err
	}
	limit, offset := int32(20), query.Page.Offset
	if query.Page.Limit > 0 {
		limit = query.Page.Limit
	}
	var total int64
	if err := s.db.WithContext(ctx).
		Model(&model.WorkVersion{}).
		Where("work_id = ?", query.WorkID).
		Count(&total).Error; err != nil {
		return nil, err
	}
	var versions []model.WorkVersion
	if err := s.db.WithContext(ctx).
		Where("work_id = ?", query.WorkID).
		Order("version DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&versions).Error; err != nil {
		return nil, err
	}
	contributorLists := make([][]string, len(versions))
	for index := range versions {
		contributorLists[index] = versions[index].ContributorMemberIDs
	}
	contributorNames, err := resolveVersionContributorNames(ctx, s.db, contributorLists...)
	if err != nil {
		return nil, err
	}
	protoVersions := make([]*managev1.WorkVersion, len(versions))
	for index := range versions {
		protoVersions[index], err = s.toProtoWorkVersion(&versions[index], contributorNames)
		if err != nil {
			return nil, domainerr.Precondition("Work Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err))
		}
	}
	return &managev1.ListWorkVersionsResponse{
		Versions: protoVersions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: int64(offset)+int64(limit) < total,
		},
	}, nil
}

// RestoreWorkVersion replaces the full source-owned graph and locale metadata
// from one immutable checkpoint. Existing target locale rows and translation
// jobs remain unchanged, including when the checkpoint switches source-locale
// authority. Global Work fields remain current.
func (s *Service) RestoreWorkVersion(ctx context.Context, command RestoreWorkVersionCommand) (*managev1.Work, error) {
	version, err := s.loadWorkVersionForRestore(ctx, command.VersionID, command.WorkID)
	if err != nil {
		return nil, err
	}
	envelope, document, err := DecodeVersionContentSnapshot(version.ContentSnapshot)
	if err != nil {
		return nil, domainerr.Precondition("Work Version typed snapshot is invalid").WithCause(failurecause.ServerFault(err))
	}
	if envelope.Title == nil || strings.TrimSpace(*envelope.Title) == "" {
		return nil, domainerr.Precondition("Work Version source title is invalid")
	}
	now := time.Now().UTC()
	var restore versionrestore.Restore
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[versionrestore.Outcome]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			var err error
			restore, err = s.lockWorkVersionRestore(ctx, tx, require, version.WorkID, envelope, document, now)
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (versionrestore.Outcome, error) {
			outcome, err := restore.Apply(ctx, tx)
			if err != nil || !outcome.Changed() {
				return outcome, err
			}
			return outcome, touchWork(ctx, tx, version.WorkID, now)
		},
		Audit: func(outcome versionrestore.Outcome) mutation.Audit {
			if !outcome.Changed() {
				return mutation.Unchanged()
			}
			return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkVersionRestoreAuditRecord(metadata, version.WorkID, version.ID)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, outcome versionrestore.Outcome) error {
			if !outcome.Changed() {
				return nil
			}
			return s.publishWorkVersionRestore(ctx, tx, version, envelope.SourceLocale, outcome)
		},
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
func (s *Service) lockWorkVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	require mutation.Require,
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
		return versionrestore.Restore{}, domainerr.Precondition("Work content document changed during version restore")
	}
	if _, err := requireLockedWorkPermission(ctx, tx, require, workID, policyv1.Work.Edit, workAuthorizationMutation); err != nil {
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

// publishWorkVersionRestore requests the OG image and signals a restore
// that changed the Work.
func (s *Service) publishWorkVersionRestore(
	ctx context.Context,
	tx *gorm.DB,
	version model.WorkVersion,
	sourceLocale string,
	outcome versionrestore.Outcome,
) error {
	if outcome.RefreshesOG() {
		if _, err := s.requestWorkOG(ctx, tx, version.WorkID, sourceLocale, "work_version_restored"); err != nil {
			return err
		}
	}
	return contentsignal.PublishInTransaction(ctx, tx, s.signals, buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID: version.WorkID, Fields: outcome.Fields(),
		DocumentRevision: outcome.DocumentRevision.String(), Contributors: versionrestore.Contributors(ctx),
		Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, Locale: sourceLocale,
		DocumentStateChanged: true,
	}))
}

func (s *Service) loadWorkVersionForRestore(
	ctx context.Context,
	versionID string,
	expectedWorkID string,
) (model.WorkVersion, error) {
	var version model.WorkVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", versionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.WorkVersion{}, domainerr.ResourceNotFound("work_version", versionID)
		}
		return model.WorkVersion{}, err
	}
	if expectedWorkID != "" && version.WorkID != expectedWorkID {
		return model.WorkVersion{}, domainerr.ResourceNotFound("work_version", versionID)
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
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domainerr.Precondition("Work source locale changed during version restore")
	}
	return nil
}

func (s *Service) loadRestoredWork(ctx context.Context, workID string) (model.Work, error) {
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", workID).Error; err != nil {
		return model.Work{}, err
	}
	source, err := LoadRequiredSourceLocaleMetadata(ctx, s.db, work.ID)
	if err != nil {
		return model.Work{}, err
	}
	if source.Title == nil {
		return model.Work{}, domainerr.Precondition("Work source title is not initialized")
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

func (s *Service) toProtoWorkVersion(
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
