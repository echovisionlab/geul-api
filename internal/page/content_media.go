package page

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type publicationFileRow struct {
	ID                string     `gorm:"column:id"`
	MIMEType          string     `gorm:"column:mime_type"`
	DurationSeconds   *int       `gorm:"column:duration_seconds"`
	DeleteRequestedAt *time.Time `gorm:"column:delete_requested_at"`
}

type publicationSourceAssetRow struct {
	SourceFileID string `gorm:"column:source_file_id"`
	Kind         string `gorm:"column:kind"`
	FileSize     *int64 `gorm:"column:file_size"`
	SHA256       []byte `gorm:"column:sha256"`
}

type publicationDerivativeRow struct {
	FileID                  string  `gorm:"column:file_id"`
	Type                    string  `gorm:"column:type"`
	AssetStatus             *string `gorm:"column:asset_status"`
	AssetFileSize           *int64  `gorm:"column:asset_file_size"`
	AssetSHA256             []byte  `gorm:"column:asset_sha256"`
	MediaGenerationFileID   *string `gorm:"column:media_generation_file_id"`
	MediaGenerationStatus   *string `gorm:"column:media_generation_status"`
	MediaGenerationManifest *string `gorm:"column:media_generation_manifest"`
}

func RequireIndexedContentBlockAttachmentsReadyForPublication(
	ctx context.Context,
	tx *gorm.DB,
	attachments []contentblock.PublicationAttachment,
) error {
	fileIDs, err := publicationFileIDs(attachments)
	if err != nil || len(fileIDs) == 0 {
		return err
	}
	var files []publicationFileRow
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Table("file").Select("id, mime_type, duration_seconds, delete_requested_at").
		Where("id IN ?", fileIDs).Order("id ASC").Find(&files).Error; err != nil {
		return err
	}
	if len(files) != len(fileIDs) {
		return domainerr.Precondition("Page content Block media File is unavailable")
	}
	readiness, err := loadPublicationReadiness(ctx, tx, fileIDs)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := readiness.require(file); err != nil {
			return err
		}
	}
	return nil
}

// publicationFileIDs returns the sorted distinct Files the attachments
// reference; a missing attachment rejects publication.
func publicationFileIDs(attachments []contentblock.PublicationAttachment) ([]string, error) {
	fileIDs := make([]string, 0, len(attachments))
	seen := make(map[string]struct{}, len(attachments))
	for _, attachment := range attachments {
		if attachment.FileID == uuid.Nil || attachment.MissingMediaKind != "" {
			return nil, domainerr.Precondition("Page content Block has a missing media attachment")
		}
		fileID := attachment.FileID.String()
		if _, exists := seen[fileID]; !exists {
			seen[fileID] = struct{}{}
			fileIDs = append(fileIDs, fileID)
		}
	}
	sort.Strings(fileIDs)
	return fileIDs, nil
}

// publicationReadiness is the ready presentation of each File, keyed by
// File ID and asset kind or derivative type.
type publicationReadiness struct {
	sourceAssets map[string]bool
	derivatives  map[string]bool
}

func loadPublicationReadiness(ctx context.Context, tx *gorm.DB, fileIDs []string) (publicationReadiness, error) {
	var sourceAssets []publicationSourceAssetRow
	if err := tx.WithContext(ctx).Table("public_asset").
		Select("source_file_id, kind, file_size, sha256").
		Where("source_file_id IN ? AND kind IN ? AND status = ?", fileIDs, []string{"image", "mesh"}, model.PublicAssetStatusReady).
		Find(&sourceAssets).Error; err != nil {
		return publicationReadiness{}, err
	}
	readiness := publicationReadiness{
		sourceAssets: make(map[string]bool, len(sourceAssets)),
	}
	for _, asset := range sourceAssets {
		if asset.FileSize != nil && len(asset.SHA256) == 32 {
			readiness.sourceAssets[asset.SourceFileID+"\x00"+asset.Kind] = true
		}
	}
	var derivatives []publicationDerivativeRow
	if err := tx.WithContext(ctx).Table("file_derivative AS fd").Select(`
		fd.file_id, fd.type, pa.status AS asset_status, pa.file_size AS asset_file_size,
		pa.sha256 AS asset_sha256, mg.file_id AS media_generation_file_id,
		mg.status AS media_generation_status, mg.manifest_name AS media_generation_manifest`).
		Joins("LEFT JOIN public_asset AS pa ON pa.id = fd.asset_id").
		Joins("LEFT JOIN media_generation AS mg ON mg.id = fd.media_generation_id").
		Where("fd.file_id IN ?", fileIDs).Find(&derivatives).Error; err != nil {
		return publicationReadiness{}, err
	}
	readiness.derivatives = make(map[string]bool, len(derivatives))
	for _, derivative := range derivatives {
		readiness.derivatives[derivative.FileID+"\x00"+derivative.Type] = derivative.ready()
	}
	return readiness, nil
}

// ready reports whether the derivative can be delivered: an HLS stream needs
// its ready manifest, any other derivative its ready asset.
func (d publicationDerivativeRow) ready() bool {
	if d.Type == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String() {
		return d.MediaGenerationFileID != nil && *d.MediaGenerationFileID == d.FileID &&
			d.MediaGenerationStatus != nil && *d.MediaGenerationStatus == model.MediaGenerationStatusReady &&
			d.MediaGenerationManifest != nil && strings.TrimSpace(*d.MediaGenerationManifest) != ""
	}
	return d.AssetStatus != nil && *d.AssetStatus == model.PublicAssetStatusReady &&
		d.AssetFileSize != nil && len(d.AssetSHA256) == 32
}

// require rejects a File whose presentation for its media type is not ready.
func (r publicationReadiness) require(file publicationFileRow) error {
	if file.DeleteRequestedAt != nil {
		return domainerr.Precondition("Page content Block media File is pending deletion")
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(file.MIMEType, ";", 2)[0]))
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		if !r.sourceAssets[file.ID+"\x00image"] {
			return domainerr.Precondition("Page content Block image presentation is not ready")
		}
	case mimeType == "model/gltf-binary":
		if !r.sourceAssets[file.ID+"\x00mesh"] {
			return domainerr.Precondition("Page content Block mesh presentation is not ready")
		}
	case strings.HasPrefix(mimeType, "video/"):
		if file.DurationSeconds == nil {
			return domainerr.Precondition("Page content Block video presentation is not ready")
		}
		return r.requireDerivatives(file.ID,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL,
		)
	case strings.HasPrefix(mimeType, "audio/"):
		if file.DurationSeconds == nil {
			return domainerr.Precondition("Page content Block audio presentation is not ready")
		}
		return r.requireDerivatives(file.ID,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM,
		)
	}
	return nil
}

func (r publicationReadiness) requireDerivatives(fileID string, derivativeTypes ...managev1.FileDerivativeType) error {
	for _, derivativeType := range derivativeTypes {
		if !r.derivatives[fileID+"\x00"+derivativeType.String()] {
			return domainerr.Precondition("Page content Block media presentation is not ready")
		}
	}
	return nil
}
