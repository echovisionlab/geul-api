package filemediapostgres

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IngestTranscodes interface {
	RequirePublisher() error
	Enqueue(context.Context, *gorm.DB, domain.IngestTranscodeCommand) error
}

func (s *store) LockIngestFile(ctx context.Context, id string) error {
	return mediaassetpostgres.NewStore(s.tx).LockAttachableFiles(ctx, []string{id})
}

func (s *store) EnsureIngestGeneration(ctx context.Context, input mediaasset.Generation) (mediaasset.Generation, error) {
	row := model.MediaGeneration{ID: input.ID, FileID: input.FileID, Kind: input.Kind, ObjectPrefix: input.ObjectPrefix, ManifestName: input.ManifestName, Status: string(input.Status), CreatedAt: input.CreatedAt, UpdatedAt: input.UpdatedAt}
	if err := s.tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return mediaasset.Generation{}, err
	}
	return mediaassetpostgres.NewStore(s.tx).Generation(ctx, input.ID)
}

func (s *store) EnsureIngestAsset(ctx context.Context, input mediaasset.Asset) (mediaasset.Asset, error) {
	return mediaassetpostgres.NewStore(s.tx).EnsureAssetAllocation(ctx, input)
}

func (s *store) CompleteIngestGeneration(ctx context.Context, cdn string, input mediaasset.WrittenGeneration) error {
	_, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), cdn).CompleteMediaGeneration(ctx, &input)
	return err
}

func (s *store) CompleteIngestAsset(ctx context.Context, cdn string, input mediaasset.WrittenAsset) error {
	_, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), cdn).CompletePublicAsset(ctx, &input)
	return err
}

func (s *store) UpsertIngestDerivative(ctx context.Context, input domain.IngestDerivative) error {
	return s.tx.WithContext(ctx).Table("file_derivative").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "file_id"}, {Name: "type"}}, DoUpdates: clause.Assignments(structured.Fields{"asset_id": input.AssetID, "media_generation_id": input.GenerationID})}).Create(structured.Fields{"id": input.ID, "file_id": input.FileID, "type": input.Type, "asset_id": input.AssetID, "media_generation_id": input.GenerationID, "created_at": input.CreatedAt}).Error
}

func (s *store) SetClientMediaBundle(ctx context.Context, file, bundle string, duration int) error {
	return s.tx.WithContext(ctx).Model(&model.File{}).Where("id = ? AND delete_requested_at IS NULL", file).Updates(structured.Fields{"duration_seconds": duration, "client_media_bundle_id": bundle}).Error
}

func (s *store) ReadyIngestDerivativeCount(ctx context.Context, file string, required []string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("file_derivative AS d").Joins("LEFT JOIN media_generation AS g ON g.id = d.media_generation_id").Joins("LEFT JOIN public_asset AS artifact ON artifact.id = d.asset_id").Where("d.file_id = ? AND d.type IN ? AND ((g.file_id = ? AND g.status = ?) OR (artifact.source_file_id = ? AND artifact.status = ?))", file, required, file, model.MediaGenerationStatusReady, file, model.PublicAssetStatusReady).Count(&count).Error
	return count, err
}

func (s *store) AttachIngestTrackAudio(ctx context.Context, input domain.TrackOriginalAudioInput) (domain.TrackOriginalAudioAttachment, error) {
	port, ok := s.ports.UploadTracks.(interface {
		AttachOriginalWithDB(context.Context, *gorm.DB, domain.TrackOriginalAudioInput) (domain.TrackOriginalAudioAttachment, error)
	})
	if !ok {
		return domain.TrackOriginalAudioAttachment{}, fmt.Errorf("Track original audio attachment is required")
	}
	return port.AttachOriginalWithDB(ctx, s.tx, input)
}

func (s *store) AttachedIngestTrackRelease(ctx context.Context, track, file string) (string, error) {
	port, ok := s.ports.UploadTracks.(interface {
		CurrentReleaseForAudio(context.Context, *gorm.DB, string, string) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("Track original audio projection is required")
	}
	return port.CurrentReleaseForAudio(ctx, s.tx, track, file)
}

func (s *store) RequireIngestTranscodePublisher(context.Context) error {
	if s.ports.IngestTranscodes == nil {
		return fmt.Errorf("transcode publisher does not register File ingest jobs")
	}
	return s.ports.IngestTranscodes.RequirePublisher()
}

func (s *store) EnqueueIngestTranscode(ctx context.Context, command domain.IngestTranscodeCommand) error {
	if err := s.RequireIngestTranscodePublisher(ctx); err != nil {
		return err
	}
	return s.ports.IngestTranscodes.Enqueue(ctx, s.tx, command)
}
