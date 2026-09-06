package release

import (
	"context"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func loadTrackWaveformRefs(
	ctx context.Context,
	db *gorm.DB,
	mediaDomain string,
	fileIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	return loadTrackStaticAssetRefs(
		ctx,
		db,
		mediaDomain,
		fileIDs,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM,
	)
}

func loadTrackSpectrogramRefs(
	ctx context.Context,
	db *gorm.DB,
	mediaDomain string,
	fileIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	return loadTrackStaticAssetRefs(
		ctx,
		db,
		mediaDomain,
		fileIDs,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM,
	)
}

func loadTrackStaticAssetRefs(
	ctx context.Context,
	db *gorm.DB,
	mediaDomain string,
	fileIDs []string,
	derivativeType managev1.FileDerivativeType,
) (map[string]*commonv1.AssetRef, error) {
	ids := uniqueNonEmptyIDs(fileIDs)
	if len(ids) == 0 {
		return map[string]*commonv1.AssetRef{}, nil
	}

	var rows []trackStaticDerivativeRow
	if err := db.WithContext(ctx).
		Table("file_derivative AS fd").
		Select("fd.file_id, "+readyPublicAssetSelect("pa")).
		Joins("JOIN public_asset AS pa ON pa.id = fd.asset_id AND pa.status = 'ready'").
		Where("fd.type = ? AND fd.file_id IN ?", derivativeType.String(), ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	return projectTrackStaticDerivativeRefs(mediaDomain, rows)
}

func loadTrackHLSRefs(
	ctx context.Context,
	db *gorm.DB,
	mediaDomain string,
	fileIDs []string,
) (map[string]*commonv1.HlsMediaRef, error) {
	ids := uniqueNonEmptyIDs(fileIDs)
	if len(ids) == 0 {
		return map[string]*commonv1.HlsMediaRef{}, nil
	}

	var rows []trackHLSDerivativeRow
	if err := db.WithContext(ctx).
		Table("file_derivative AS fd").
		Select("fd.file_id, mg.id AS generation_id, mg.manifest_name").
		Joins("JOIN media_generation AS mg ON mg.id = fd.media_generation_id AND mg.file_id = fd.file_id AND mg.status = 'ready'").
		Where("fd.type = ? AND fd.file_id IN ?", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	return projectTrackHLSRefs(mediaDomain, rows)
}

type trackStaticDerivativeRow struct {
	FileID string              `gorm:"column:file_id"`
	Asset  readyPublicAssetRow `gorm:"embedded"`
}

type trackHLSDerivativeRow struct {
	FileID       string `gorm:"column:file_id"`
	GenerationID string `gorm:"column:generation_id"`
	ManifestName string `gorm:"column:manifest_name"`
}

func uniqueNonEmptyIDs(fileIDs []string) []string {
	ids := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, fileID := range fileIDs {
		if fileID == "" {
			continue
		}
		if _, ok := seen[fileID]; ok {
			continue
		}
		seen[fileID] = struct{}{}
		ids = append(ids, fileID)
	}
	return ids
}

func projectTrackStaticDerivativeRefs(mediaDomain string, rows []trackStaticDerivativeRow) (map[string]*commonv1.AssetRef, error) {
	result := make(map[string]*commonv1.AssetRef, len(rows))
	for _, row := range rows {
		ref, err := projectReadyPublicAsset(mediaDomain, row.Asset)
		if err != nil {
			return nil, err
		}
		result[row.FileID] = ref
	}
	return result, nil
}

func projectTrackHLSRefs(
	mediaDomain string,
	rows []trackHLSDerivativeRow,
) (map[string]*commonv1.HlsMediaRef, error) {
	result := make(map[string]*commonv1.HlsMediaRef, len(rows))
	for _, row := range rows {
		ref, err := buildPublicHLSRef(mediaDomain, readyMediaGenerationRow(row))
		if err != nil {
			return nil, err
		}
		result[row.FileID] = ref
	}
	return result, nil
}
