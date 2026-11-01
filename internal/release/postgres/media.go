package releasepostgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func (s *store) FileID(ctx context.Context, fileID string) (string, error) {
	var file model.File
	if err := s.tx.WithContext(ctx).First(&file, "id = ?", fileID).Error; err != nil {
		return "", notFoundAs(err, release.ErrFileNotFound)
	}
	return file.ID, nil
}

func (s *store) ArtworkFile(ctx context.Context, releaseID string) (string, bool, error) {
	var existing model.ReleaseFile
	err := s.tx.WithContext(ctx).Where("release_id = ?", releaseID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return existing.FileID, true, nil
}

func (s *store) SetArtworkFile(ctx context.Context, releaseID, fileID string) error {
	if err := s.ClearArtworkFiles(ctx, releaseID); err != nil {
		return err
	}
	return s.tx.WithContext(ctx).Create(&model.ReleaseFile{ReleaseID: releaseID, FileID: fileID, SortOrder: 0}).Error
}

func (s *store) ClearArtworkFiles(ctx context.Context, releaseID string) error {
	return s.tx.WithContext(ctx).Where("release_id = ?", releaseID).Delete(&model.ReleaseFile{}).Error
}

func (s *store) LockAttachableFiles(ctx context.Context, fileIDs []string) error {
	return s.ports.Assets.LockAttachableFiles(ctx, s.tx, fileIDs)
}

func (s *store) BindArtwork(ctx context.Context, fileID, releaseID string) (*commonv1.AssetRef, error) {
	return s.ports.Assets.BindArtwork(ctx, s.tx, fileID, releaseID)
}

func (s *store) ReleaseArtworkBinding(ctx context.Context, releaseID string) error {
	return s.ports.Assets.ReleaseArtwork(ctx, s.tx, releaseID)
}

func (s *store) CancelOG(ctx context.Context, releaseID string) error {
	return s.ports.OG.CancelAndRelease(ctx, s.tx, releaseID)
}

func (s *store) ReadyArtwork(ctx context.Context, releaseID string) (*commonv1.AssetRef, error) {
	var result struct {
		FileID string `gorm:"column:file_id"`
	}
	err := s.tx.WithContext(ctx).
		Table("release_file rf").
		Select("rf.file_id").
		Where("rf.release_id = ?", releaseID).
		Order("rf.sort_order").
		Take(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.ports.Assets.ReadyArtwork(ctx, s.tx, result.FileID)
}

func (s *store) ReadyArtworks(ctx context.Context, releaseIDs []string) (map[string]*commonv1.AssetRef, error) {
	type row struct {
		ReleaseID string `gorm:"column:release_id"`
		AssetID   string `gorm:"column:asset_id"`
	}
	result := make(map[string]*commonv1.AssetRef, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}
	var rows []row
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (rf.release_id)
			rf.release_id,
			pa.id AS asset_id
		FROM release_file AS rf
		JOIN public_asset AS pa
		  ON pa.source_file_id = rf.file_id
		 AND pa.kind = 'artwork'
		 AND pa.status = 'ready'
		WHERE rf.release_id IN ?
		ORDER BY rf.release_id, rf.sort_order ASC, pa.created_at DESC, pa.id DESC
	`, releaseIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	candidates := make([]*string, 0, len(rows))
	for i := range rows {
		candidates = append(candidates, &rows[i].AssetID)
	}
	assets, err := s.ports.Assets.ReadyAssets(ctx, s.tx, candidates...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ReleaseID] = assets[row.AssetID]
	}
	return result, nil
}
