package release

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SetReleaseArtworkCommand makes an existing File the Release artwork.
type SetReleaseArtworkCommand struct {
	ReleaseID string
	FileID    string
}

// DeleteReleaseArtworkCommand removes the Release artwork relation without
// deleting the File.
type DeleteReleaseArtworkCommand struct {
	ReleaseID string
}

// artworkChange is the File an artwork mutation attached or removed; FileID
// is empty when nothing changed.
type artworkChange struct {
	FileID string
	Asset  *commonv1.AssetRef
}

// SetReleaseArtwork sets the release artwork and returns its ready asset, or
// nil when the File already was the artwork.
func (s *Service) SetReleaseArtwork(ctx context.Context, command SetReleaseArtworkCommand) (*commonv1.AssetRef, error) {
	if err := s.requireArtworkRelease(ctx, command.ReleaseID); err != nil {
		return nil, err
	}
	var file model.File
	if err := s.db.WithContext(ctx).First(&file, "id = ?", command.FileID).Error; err != nil {
		return nil, missingAs(err, domainerr.ResourceNotFound("file", command.FileID))
	}
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[artworkChange]{
		Lock:   lockReleaseRow(command.ReleaseID, nil),
		Access: s.releaseAccess(command.ReleaseID, releaseActionEdit),
		Change: func(ctx context.Context, tx *gorm.DB) (artworkChange, error) {
			return s.replaceReleaseArtwork(ctx, tx, command.ReleaseID, command.FileID, file.ID)
		},
		Audit: func(change artworkChange) mutation.Audit {
			return releaseArtworkAudit(command.ReleaseID, change.FileID, sharedtelemetry.AuditCollectionOperationAdded)
		},
		Outbox: s.publishArtworkChange(command.ReleaseID),
	})
	return change.Asset, err
}

// DeleteReleaseArtwork removes the release artwork; an absent artwork is
// not an error.
func (s *Service) DeleteReleaseArtwork(ctx context.Context, command DeleteReleaseArtworkCommand) error {
	if err := s.requireArtworkRelease(ctx, command.ReleaseID); err != nil {
		return err
	}
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[artworkChange]{
		Lock:   lockReleaseRow(command.ReleaseID, nil),
		Access: s.releaseAccess(command.ReleaseID, releaseActionEdit),
		Change: func(ctx context.Context, tx *gorm.DB) (artworkChange, error) {
			return s.removeReleaseArtwork(ctx, tx, command.ReleaseID)
		},
		Audit: func(change artworkChange) mutation.Audit {
			return releaseArtworkAudit(command.ReleaseID, change.FileID, sharedtelemetry.AuditCollectionOperationRemoved)
		},
		Outbox: s.publishArtworkChange(command.ReleaseID),
	})
	return err
}

// requireArtworkRelease answers not found for a missing Release before the
// File is looked up, and fails like a read when its content document cannot
// be projected.
func (s *Service) requireArtworkRelease(ctx context.Context, releaseID string) error {
	var release model.Release
	if err := s.db.WithContext(ctx).Select("id").First(&release, "id = ?", releaseID).Error; err != nil {
		return missingAs(err, releaseNotFound(releaseID))
	}
	_, err := s.overlayReleaseSourceLocaleDocument(ctx, &release)
	return err
}

// replaceReleaseArtwork attaches the requested File; storedFileID is the same
// File as the File row reads it.
func (s *Service) replaceReleaseArtwork(ctx context.Context, tx *gorm.DB, releaseID, fileID, storedFileID string) (artworkChange, error) {
	if err := s.assets.LockAttachableFiles(ctx, tx, []string{fileID}); err != nil {
		return artworkChange{}, err
	}
	var existing model.ReleaseFile
	err := tx.Where("release_id = ?", releaseID).First(&existing).Error
	if err == nil && existing.FileID == fileID {
		return artworkChange{}, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return artworkChange{}, err
	}
	if err := tx.Where("release_id = ?", releaseID).Delete(&model.ReleaseFile{}).Error; err != nil {
		return artworkChange{}, err
	}
	if err := tx.Create(&model.ReleaseFile{ReleaseID: releaseID, FileID: fileID, SortOrder: 0}).Error; err != nil {
		return artworkChange{}, err
	}
	if err := touchRelease(tx, releaseID); err != nil {
		return artworkChange{}, err
	}
	asset, err := s.assets.BindArtwork(ctx, tx, storedFileID, releaseID)
	if err != nil {
		return artworkChange{}, err
	}
	if err := s.og.CancelAndRelease(ctx, tx, releaseID); err != nil {
		return artworkChange{}, err
	}
	return artworkChange{FileID: fileID, Asset: asset}, nil
}

func (s *Service) removeReleaseArtwork(ctx context.Context, tx *gorm.DB, releaseID string) (artworkChange, error) {
	var existing model.ReleaseFile
	if err := tx.Where("release_id = ?", releaseID).First(&existing).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return artworkChange{}, nil
	} else if err != nil {
		return artworkChange{}, err
	}
	if err := tx.Where("release_id = ?", releaseID).Delete(&model.ReleaseFile{}).Error; err != nil {
		return artworkChange{}, err
	}
	if err := touchRelease(tx, releaseID); err != nil {
		return artworkChange{}, err
	}
	if err := s.assets.ReleaseArtwork(ctx, tx, releaseID); err != nil {
		return artworkChange{}, err
	}
	if err := s.og.CancelAndRelease(ctx, tx, releaseID); err != nil {
		return artworkChange{}, err
	}
	return artworkChange{FileID: existing.FileID}, nil
}

// touchRelease marks the Release row changed by a relation mutation.
func touchRelease(tx *gorm.DB, releaseID string) error {
	return tx.Model(&model.Release{}).Where("id = ?", releaseID).Update("updated_at", time.Now()).Error
}

func releaseArtworkAudit(releaseID, fileID string, op sharedtelemetry.AuditCollectionOperation) mutation.Audit {
	if fileID == "" {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseArtworkAuditRecord(m, releaseID, fileID, op)
	})
}

func (s *Service) publishArtworkChange(releaseID string) func(context.Context, *gorm.DB, artworkChange) error {
	return func(ctx context.Context, tx *gorm.DB, change artworkChange) error {
		if change.FileID == "" {
			return nil
		}
		return s.publishContentUpdated(ctx, tx, buildReleaseMediaMutationContentUpdatedEvent(releaseID, "media.artwork"))
	}
}
