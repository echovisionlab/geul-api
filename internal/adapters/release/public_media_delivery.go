package release

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediamime"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/sliceutil"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

func loadReadyPublicAssetsForSourceFiles(
	ctx context.Context,
	db *gorm.DB,
	sourceFileIDs []string,
	kinds ...string,
) (map[string]readyPublicAssetRow, error) {
	ids := sliceutil.UniqueNonZero(sourceFileIDs)
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

func projectReadyPublicAsset(cdnDomain string, row readyPublicAssetRow) (*mediaasset.Reference, error) {
	if strings.TrimSpace(row.AssetID) == "" || strings.TrimSpace(row.Extension) == "" || strings.TrimSpace(row.MimeType) == "" {
		return nil, errors.New("ready public asset metadata is incomplete")
	}
	if row.FileSize <= 0 || len(row.SHA256) != 32 {
		return nil, errors.New("ready public asset integrity metadata is incomplete")
	}
	if expected := mediamime.GetExtensionFromMime(row.MimeType); expected == "bin" || expected != row.Extension {
		return nil, errors.New("ready public asset extension does not match MIME type")
	}
	if row.Kind == "attachment" || row.Disposition != "inline" {
		return nil, errors.New("attachment public assets cannot be emitted")
	}
	assetPath, err := mediaauth.AssetPath(row.AssetID, row.Kind, row.Extension)
	if err != nil {
		return nil, err
	}
	return &mediaasset.Reference{
		AssetID:     row.AssetID,
		URL:         joinPublicCDNPath(cdnDomain, assetPath),
		Extension:   row.Extension,
		MimeType:    row.MimeType,
		FileSize:    row.FileSize,
		SHA256:      append([]byte(nil), row.SHA256...),
		Disposition: mediaasset.DispositionInline,
	}, nil
}

func buildPublicHLSRef(mediaDomain string, row readyMediaGenerationRow) (*commonv1.HlsMediaRef, error) {
	url, err := mediaauth.PublicHLSURL(mediaDomain, row.FileID, row.GenerationID, row.ManifestName)
	if err != nil {
		return nil, err
	}
	return &commonv1.HlsMediaRef{
		FileId:       row.FileID,
		GenerationId: row.GenerationID,
		Url:          url,
	}, nil
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
