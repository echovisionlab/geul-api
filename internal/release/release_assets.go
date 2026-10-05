package release

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ReleaseService implements the ReleaseService Connect handler
func (s *ReleaseService) SetReleaseArtwork(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseArtworkRequest],
) (*connect.Response[managev1.SetReleaseArtworkResponse], error) {
	var release model.Release
	if err := s.db.WithContext(ctx).
		Select("id").
		First(&release, "id = ?", req.Msg.ReleaseId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("release", req.Msg.ReleaseId)
		}
		return nil, errs.Internal(err)
	}
	if err := s.overlayReleaseSourceLocaleDocument(ctx, &release); err != nil {
		return nil, err
	}

	// Verify file exists
	var file model.File
	if err := s.db.WithContext(ctx).First(&file, "id = ?", req.Msg.FileId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("file", req.Msg.FileId)
		}
		return nil, errs.Internal(err)
	}

	// Use transaction to ensure atomicity and prevent race conditions
	var artworkAsset *commonv1.AssetRef
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			First(&release, "id = ?", req.Msg.ReleaseId).Error; err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, req.Msg.ReleaseId, releaseActionEdit); err != nil {
			return err
		}
		if err := s.assets.LockAttachableFiles(ctx, tx, []string{req.Msg.FileId}); err != nil {
			return err
		}
		var existing model.ReleaseFile
		err := tx.Where("release_id = ?", req.Msg.ReleaseId).First(&existing).Error
		if err == nil && existing.FileID == req.Msg.FileId {
			return nil
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		// Delete existing artwork (DB record)
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).
			Delete(&model.ReleaseFile{}).Error; err != nil {
			return err
		}

		// Create new artwork entry
		releaseFile := model.ReleaseFile{
			ReleaseID: req.Msg.ReleaseId,
			FileID:    req.Msg.FileId,
			SortOrder: 0,
		}
		if err := tx.Create(&releaseFile).Error; err != nil {
			return err
		}

		// Update release updated_at
		if err := tx.Model(&model.Release{}).
			Where("id = ?", req.Msg.ReleaseId).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}

		artworkRef, err := s.assets.BindArtwork(ctx, tx, file.ID, req.Msg.ReleaseId)
		if err != nil {
			return err
		}
		artworkAsset = artworkRef
		if err := s.og.CancelAndRelease(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		return s.appendReleaseArtworkAudit(ctx, tx, req.Msg.ReleaseId, req.Msg.FileId, sharedtelemetry.AuditCollectionOperationAdded)
	})

	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	publishContentUpdatedEvent(
		ctx,
		s.asyncPublisher,
		buildReleaseMediaMutationContentUpdatedEvent(
			release.ID,
			"media.artwork",
		),
	)

	return connect.NewResponse(&managev1.SetReleaseArtworkResponse{
		ArtworkAsset: artworkAsset,
	}), nil
}

// DeleteReleaseArtwork deletes the release artwork
func (s *ReleaseService) DeleteReleaseArtwork(
	ctx context.Context,
	req *connect.Request[managev1.DeleteReleaseArtworkRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	var release model.Release
	if err := s.db.WithContext(ctx).
		Select("id").
		First(&release, "id = ?", req.Msg.ReleaseId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("release", req.Msg.ReleaseId)
		}
		return nil, errs.Internal(err)
	}
	if err := s.overlayReleaseSourceLocaleDocument(ctx, &release); err != nil {
		return nil, err
	}

	// Use transaction to ensure atomicity and prevent race conditions
	deleted := false
	var removedFileID string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			First(&release, "id = ?", req.Msg.ReleaseId).Error; err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, req.Msg.ReleaseId, releaseActionEdit); err != nil {
			return err
		}
		var existing model.ReleaseFile
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).First(&existing).Error; err == gorm.ErrRecordNotFound {
			return nil
		} else if err != nil {
			return err
		}
		removedFileID = existing.FileID
		deleted = true
		// Delete the release_file record
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).
			Delete(&model.ReleaseFile{}).Error; err != nil {
			return err
		}

		// Update release updated_at
		if err := tx.Model(&model.Release{}).
			Where("id = ?", req.Msg.ReleaseId).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}

		if err := s.assets.ReleaseArtwork(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		if err := s.og.CancelAndRelease(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		return s.appendReleaseArtworkAudit(ctx, tx, req.Msg.ReleaseId, removedFileID, sharedtelemetry.AuditCollectionOperationRemoved)
	})

	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if deleted {
		publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			buildReleaseMediaMutationContentUpdatedEvent(
				release.ID,
				"media.artwork",
			),
		)
	}

	return connect.NewResponse(&managev1.OgAssetDeleteResponse{
		Success: true,
	}), nil
}

// =============================================================================
// Utility
// =============================================================================

// CheckReleaseSlugAvailable checks if a slug is available (Site Admin only).
