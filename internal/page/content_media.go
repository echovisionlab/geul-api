package page

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type contentBlockMediaReferenceRow struct {
	BlockID       string `gorm:"column:block_id"`
	ReferencePath string `gorm:"column:reference_path"`
	FileID        string `gorm:"column:file_id"`
}

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

// LoadContentBlockMediaReferences loads the active Page Block attachment
// selectors after the owning Page read has authorized the document.
func LoadContentBlockMediaReferences(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if documentID == uuid.Nil {
		return []*contentv1.ContentBlockMediaItem{}, nil
	}
	var rows []contentBlockMediaReferenceRow
	if err := db.WithContext(ctx).Raw(`
		SELECT attachment.block_id, attachment.reference_path, attachment.file_id
		FROM content_block_attachment AS attachment
		JOIN content_block AS block ON block.id = attachment.block_id
		WHERE block.document_id = ? AND attachment.selector_kind = 'active'
		ORDER BY attachment.block_id ASC, attachment.reference_path ASC
	`, documentID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load Page Content Block attachment references: %w", err)
	}
	items := make([]*contentv1.ContentBlockMediaItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, &contentv1.ContentBlockMediaItem{
			Selector:   &contentv1.ContentBlockMediaSelector{BlockId: row.BlockID, ReferencePath: row.ReferencePath},
			Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: row.FileID}},
		})
	}
	return items, nil
}

func RequireIndexedContentBlockAttachmentsReadyForPublication(
	ctx context.Context,
	tx *gorm.DB,
	attachments []contentblock.PublicationAttachment,
) error {
	fileIDs := make([]string, 0, len(attachments))
	seen := make(map[string]struct{}, len(attachments))
	for _, attachment := range attachments {
		if attachment.FileID == uuid.Nil || attachment.MissingMediaKind != "" {
			return errs.FailedPrecondition("Page content Block has a missing media attachment")
		}
		fileID := attachment.FileID.String()
		if _, exists := seen[fileID]; !exists {
			seen[fileID] = struct{}{}
			fileIDs = append(fileIDs, fileID)
		}
	}
	if len(fileIDs) == 0 {
		return nil
	}
	sort.Strings(fileIDs)
	var files []publicationFileRow
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "SHARE"}).
		Table("file").Select("id, mime_type, duration_seconds, delete_requested_at").
		Where("id IN ?", fileIDs).Order("id ASC").Find(&files).Error; err != nil {
		return errs.Internal(err)
	}
	if len(files) != len(fileIDs) {
		return errs.FailedPrecondition("Page content Block media File is unavailable")
	}
	var sourceAssets []publicationSourceAssetRow
	if err := tx.WithContext(ctx).Table("public_asset").
		Select("source_file_id, kind, file_size, sha256").
		Where("source_file_id IN ? AND kind IN ? AND status = ?", fileIDs, []string{"image", "mesh"}, model.PublicAssetStatusReady).
		Find(&sourceAssets).Error; err != nil {
		return errs.Internal(err)
	}
	readySourceAssets := make(map[string]bool, len(sourceAssets))
	for _, asset := range sourceAssets {
		if asset.FileSize != nil && len(asset.SHA256) == 32 {
			readySourceAssets[asset.SourceFileID+"\x00"+asset.Kind] = true
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
		return errs.Internal(err)
	}
	readyDerivatives := make(map[string]bool, len(derivatives))
	for _, derivative := range derivatives {
		key := derivative.FileID + "\x00" + derivative.Type
		if derivative.Type == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String() {
			readyDerivatives[key] = derivative.MediaGenerationFileID != nil && *derivative.MediaGenerationFileID == derivative.FileID &&
				derivative.MediaGenerationStatus != nil && *derivative.MediaGenerationStatus == model.MediaGenerationStatusReady &&
				derivative.MediaGenerationManifest != nil && strings.TrimSpace(*derivative.MediaGenerationManifest) != ""
			continue
		}
		readyDerivatives[key] = derivative.AssetStatus != nil && *derivative.AssetStatus == model.PublicAssetStatusReady &&
			derivative.AssetFileSize != nil && len(derivative.AssetSHA256) == 32
	}
	requireDerivative := func(fileID string, derivativeType managev1.FileDerivativeType) error {
		if !readyDerivatives[fileID+"\x00"+derivativeType.String()] {
			return errs.FailedPrecondition("Page content Block media presentation is not ready")
		}
		return nil
	}
	for _, file := range files {
		if file.DeleteRequestedAt != nil {
			return errs.FailedPrecondition("Page content Block media File is pending deletion")
		}
		mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(file.MIMEType, ";", 2)[0]))
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			if !readySourceAssets[file.ID+"\x00image"] {
				return errs.FailedPrecondition("Page content Block image presentation is not ready")
			}
		case mimeType == "model/gltf-binary":
			if !readySourceAssets[file.ID+"\x00mesh"] {
				return errs.FailedPrecondition("Page content Block mesh presentation is not ready")
			}
		case strings.HasPrefix(mimeType, "video/"):
			if file.DurationSeconds == nil {
				return errs.FailedPrecondition("Page content Block video presentation is not ready")
			}
			if err := requireDerivative(file.ID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS); err != nil {
				return err
			}
			if err := requireDerivative(file.ID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL); err != nil {
				return err
			}
		case strings.HasPrefix(mimeType, "audio/"):
			if file.DurationSeconds == nil {
				return errs.FailedPrecondition("Page content Block audio presentation is not ready")
			}
			for _, derivativeType := range []managev1.FileDerivativeType{
				managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS,
				managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM,
				managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM,
			} {
				if err := requireDerivative(file.ID, derivativeType); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
