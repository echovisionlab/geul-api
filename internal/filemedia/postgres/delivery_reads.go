package filemediapostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	transcodepostgres "github.com/echovisionlab/geul-api/internal/transcode/postgres"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func (s *store) StoredFileDeliveries(ctx context.Context, fileIDs []string, withIngest bool) ([]domain.StoredFileDelivery, error) {
	fields := []string{"id", "extension", "mime_type", "file_size", "duration_seconds", "file_name"}
	if withIngest {
		fields = append(fields, "ingest_slot_id", "ingest_attempt_id", "client_media_bundle_id")
	}
	var rows []domain.StoredFileDelivery
	err := s.tx.WithContext(ctx).Table("file").Select(fields).Where("id IN ? AND delete_requested_at IS NULL", fileIDs).Find(&rows).Error
	return rows, err
}
func (s *store) StoredDerivativeDeliveries(ctx context.Context, fileIDs []string) ([]domain.StoredDerivativeDelivery, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []domain.StoredDerivativeDelivery
	err := s.tx.WithContext(ctx).
		Table("file_derivative AS fd").
		Select(`
			fd.file_id,
			fd.type,
			pa.id AS asset_id,
			pa.kind AS asset_kind,
			pa.extension AS asset_extension,
			pa.mime_type AS asset_mime_type,
			pa.file_size AS asset_file_size,
			pa.sha256 AS asset_sha256,
			pa.disposition AS asset_disposition,
			pa.download_filename AS asset_download_filename,
			pa.status AS asset_status,
			mg.id AS media_generation_id,
			mg.file_id AS media_generation_file_id,
			mg.manifest_name AS media_generation_manifest,
			mg.status AS media_generation_status`).
		Joins("LEFT JOIN public_asset pa ON pa.id = fd.asset_id").
		Joins("LEFT JOIN media_generation mg ON mg.id = fd.media_generation_id").
		Where("fd.file_id IN ?", fileIDs).
		Find(&rows).Error
	return rows, err
}
func (s *store) MeshDeliveryOutputs(ctx context.Context, fileID string) ([]domain.MeshDeliveryOutput, error) {
	var rows []domain.MeshDeliveryOutput
	if err := s.tx.WithContext(ctx).Table("mesh_optimization_candidate").
		Select("id", "status", "output_file_id").
		Where("source_file_id = ? AND output_file_id IS NOT NULL", fileID).
		Order("created_at DESC, id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil

}
func (s *store) SourceAssetReferences(ctx context.Context, cdn, kind string, ids []string) (map[string]*mediaasset.Reference, error) {
	return mediaasset.LoadReadyPublicAssetRefsForSourceFiles(ctx, mediaassetpostgres.NewUnits(s.tx), cdn, kind, ids)
}
func (s *store) DeliveryProcessingFailures(ctx context.Context, audioIDs, videoIDs []string) (domain.ProcessingFailureCounts, error) {
	var r domain.ProcessingFailureCounts
	var err error
	r.Audio, err = transcodepostgres.FailedTranscodeCounts(ctx, s.tx, audioIDs, eventpkg.QueueTranscoderAudio)
	if err != nil {
		return r, err
	}
	r.Video, err = transcodepostgres.FailedTranscodeCounts(ctx, s.tx, videoIDs, eventpkg.QueueTranscoderVideo)
	if err != nil {
		return r, err
	}
	r.Waveform, err = transcodepostgres.FailedWaveformCounts(ctx, s.tx, audioIDs)
	return r, err
}
