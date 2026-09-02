package public

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type readyPublicAssetRow struct {
	AssetID          string  `gorm:"column:asset_id"`
	SourceFileID     *string `gorm:"column:source_file_id"`
	Kind             string  `gorm:"column:kind"`
	Extension        string  `gorm:"column:extension"`
	MimeType         string  `gorm:"column:mime_type"`
	FileSize         int64   `gorm:"column:file_size"`
	SHA256           []byte  `gorm:"column:sha256"`
	Disposition      string  `gorm:"column:disposition"`
	DownloadFilename *string `gorm:"column:download_filename"`
}

type readyMediaGenerationRow struct {
	FileID       string `gorm:"column:file_id"`
	GenerationID string `gorm:"column:generation_id"`
	ManifestName string `gorm:"column:manifest_name"`
}

func uniqueNonEmptyIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func readyPublicAssetSelect(alias string) string {
	return fmt.Sprintf(`
		%s.id AS asset_id,
		%s.source_file_id,
		%s.kind,
		%s.extension,
		%s.mime_type,
		%s.file_size,
		%s.sha256,
		%s.disposition,
		%s.download_filename
	`, alias, alias, alias, alias, alias, alias, alias, alias, alias)
}

func loadReadyPublicAssetByID(
	ctx context.Context,
	db *gorm.DB,
	assetID string,
) (readyPublicAssetRow, error) {
	var row readyPublicAssetRow
	err := db.WithContext(ctx).
		Table("public_asset AS pa").
		Select(readyPublicAssetSelect("pa")).
		Where("pa.id = ? AND pa.status = 'ready'", strings.TrimSpace(assetID)).
		Take(&row).Error
	return row, err
}

func loadReadyPublicAssetsForSourceFiles(
	ctx context.Context,
	db *gorm.DB,
	sourceFileIDs []string,
	kinds ...string,
) (map[string]readyPublicAssetRow, error) {
	ids := uniqueNonEmptyIDs(sourceFileIDs)
	result := make(map[string]readyPublicAssetRow, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	query := db.WithContext(ctx).
		Table("public_asset AS pa").
		Select(readyPublicAssetSelect("pa")).
		Where("pa.source_file_id IN ? AND pa.status = 'ready'", ids)
	if len(kinds) > 0 {
		query = query.Where("pa.kind IN ?", kinds)
	}

	var rows []readyPublicAssetRow
	if err := query.Order("pa.created_at DESC, pa.id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.SourceFileID == nil {
			continue
		}
		if _, exists := result[*row.SourceFileID]; !exists {
			result[*row.SourceFileID] = row
		}
	}
	return result, nil
}

func loadReadyPublicAssetsByIDs(
	ctx context.Context,
	db *gorm.DB,
	assetIDs []string,
) (map[string]readyPublicAssetRow, error) {
	ids := uniqueNonEmptyIDs(assetIDs)
	result := make(map[string]readyPublicAssetRow, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []readyPublicAssetRow
	if err := db.WithContext(ctx).
		Table("public_asset AS pa").
		Select(readyPublicAssetSelect("pa")).
		Where("pa.id IN ? AND pa.status = 'ready'", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AssetID] = row
	}
	return result, nil
}

func loadReadyMediaGenerationsByIDs(
	ctx context.Context,
	db *gorm.DB,
	generationIDs []string,
) (map[string]readyMediaGenerationRow, error) {
	ids := uniqueNonEmptyIDs(generationIDs)
	result := make(map[string]readyMediaGenerationRow, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []readyMediaGenerationRow
	if err := db.WithContext(ctx).
		Table("media_generation AS mg").
		Select("mg.file_id, mg.id AS generation_id, mg.manifest_name").
		Where("mg.id IN ? AND mg.status = 'ready'", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.GenerationID] = row
	}
	return result, nil
}

func projectReadyPublicAsset(cdnDomain string, row readyPublicAssetRow) (*commonv1.AssetRef, error) {
	if strings.TrimSpace(row.AssetID) == "" || strings.TrimSpace(row.Extension) == "" || strings.TrimSpace(row.MimeType) == "" {
		return nil, errors.New("ready public asset metadata is incomplete")
	}
	if row.FileSize <= 0 || len(row.SHA256) != 32 {
		return nil, errors.New("ready public asset integrity metadata is incomplete")
	}
	if expected := model.GetExtensionFromMime(row.MimeType); expected == "bin" || expected != row.Extension {
		return nil, errors.New("ready public asset extension does not match MIME type")
	}
	if row.Kind == "attachment" || row.Disposition != "inline" {
		return nil, errors.New("attachment public assets cannot be emitted")
	}
	assetPath, err := mediaauth.AssetPath(row.AssetID, row.Kind, row.Extension)
	if err != nil {
		return nil, err
	}
	return &commonv1.AssetRef{
		AssetId:     row.AssetID,
		Url:         joinPublicCDNPath(cdnDomain, assetPath),
		Extension:   row.Extension,
		MimeType:    row.MimeType,
		FileSize:    row.FileSize,
		Sha256:      append([]byte(nil), row.SHA256...),
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}, nil
}

func readyAssetRefByID(ctx context.Context, db *gorm.DB, cdnDomain string, assetID string) (*commonv1.AssetRef, error) {
	row, err := loadReadyPublicAssetByID(ctx, db, assetID)
	if err != nil {
		return nil, err
	}
	return projectReadyPublicAsset(cdnDomain, row)
}

func resolvedOgAssetRef(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	for _, assetID := range []*string{localizedAssetID, sourceAssetID} {
		if assetID == nil || strings.TrimSpace(*assetID) == "" {
			continue
		}
		ref, err := readyAssetRefByID(ctx, db, cdnDomain, *assetID)
		if err == nil {
			return ref, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, nil
}

// ResolveReadyOGAsset selects the localized ready asset first and then the
// source asset. The owning public domain controls when source fallback is
// allowed by the IDs it supplies.
func (s *FileService) ResolveReadyOGAsset(
	ctx context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	return resolvedOgAssetRef(ctx, s.db, s.cdnDomain, sourceAssetID, localizedAssetID)
}

func buildExpiringFileRef(
	mediaDomain string,
	mediaSecret string,
	fileID string,
	extension string,
	mimeType string,
	fileName *string,
	purpose mediaauth.Purpose,
	ttl time.Duration,
) (*commonv1.ExpiringMediaRef, error) {
	var url string
	var err error
	var downloadFilename string
	if purpose == mediaauth.PurposeDownload {
		downloadFilename = filemedia.CanonicalDownloadFilename(fileName, fileID, extension)
		url, err = filemedia.BuildSignedMediaDownloadURL(
			mediaDomain,
			fileID,
			extension,
			mediaSecret,
			ttl,
			downloadFilename,
		)
	} else {
		url, err = filemedia.BuildSignedMediaFileURL(mediaDomain, fileID, extension, mediaSecret, ttl, purpose)
	}
	if err != nil {
		return nil, err
	}
	ref := &commonv1.ExpiringMediaRef{
		FileId:    fileID,
		Url:       url,
		Purpose:   mediaDeliveryPurpose(purpose),
		ExpiresAt: timestamppb.New(time.Now().UTC().Add(ttl)),
		Extension: extension,
		MimeType:  mimeType,
		FileName:  fileName,
	}
	if purpose == mediaauth.PurposeDownload {
		ref.FileName = &downloadFilename
	}
	return ref, nil
}

func buildPublicHLSRef(mediaDomain string, row readyMediaGenerationRow) (*commonv1.HlsMediaRef, error) {
	url, err := filemedia.BuildPublicMediaHLSURL(mediaDomain, row.FileID, row.GenerationID, row.ManifestName)
	if err != nil {
		return nil, err
	}
	return &commonv1.HlsMediaRef{
		FileId:       row.FileID,
		GenerationId: row.GenerationID,
		Url:          url,
	}, nil
}

func mediaDeliveryPurpose(purpose mediaauth.Purpose) commonv1.MediaDeliveryPurpose {
	switch purpose {
	case mediaauth.PurposeInline:
		return commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE
	case mediaauth.PurposeDownload:
		return commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD
	default:
		return commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_UNSPECIFIED
	}
}

func joinPublicCDNPath(cdnDomain string, resourcePath string) string {
	resourcePath = "/" + strings.TrimLeft(resourcePath, "/")
	cdnDomain = strings.TrimRight(strings.TrimSpace(cdnDomain), "/")
	if cdnDomain == "" {
		return resourcePath
	}
	if !strings.HasPrefix(cdnDomain, "http://") && !strings.HasPrefix(cdnDomain, "https://") {
		cdnDomain = "https://" + cdnDomain
	}
	return cdnDomain + resourcePath
}
