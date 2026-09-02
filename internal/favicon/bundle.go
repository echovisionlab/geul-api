// Package favicon owns the generated favicon bundle contract and its public
// asset projection.
package favicon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

type derivativeAssetRow struct {
	Type             string  `gorm:"column:type"`
	ID               string  `gorm:"column:id"`
	SourceFileID     *string `gorm:"column:source_file_id"`
	Kind             string  `gorm:"column:kind"`
	ObjectKey        string  `gorm:"column:object_key"`
	Extension        string  `gorm:"column:extension"`
	MimeType         string  `gorm:"column:mime_type"`
	FileSize         *int64  `gorm:"column:file_size"`
	SHA256           []byte  `gorm:"column:sha256"`
	Disposition      string  `gorm:"column:disposition"`
	DownloadFilename *string `gorm:"column:download_filename"`
	Status           string  `gorm:"column:status"`
}

// LoadSet returns nil for a legacy favicon source without generated derivatives.
// A generated set must be complete and have verified ready public assets.
func LoadSet(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	sourceFileID string,
) (*commonv1.FaviconAssetSet, error) {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if sourceFileID == "" {
		return nil, nil
	}

	var source model.File
	if err := db.WithContext(ctx).Select("id", "mime_type").Where("id = ?", sourceFileID).Take(&source).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	var rows []derivativeAssetRow
	if err := db.WithContext(ctx).Table("file_derivative AS fd").Select(`
			fd.type, pa.id, pa.source_file_id, pa.kind, pa.object_key, pa.extension, pa.mime_type,
			pa.file_size, pa.sha256, pa.disposition, pa.download_filename, pa.status`).
		Joins("LEFT JOIN public_asset AS pa ON pa.id = fd.asset_id").
		Where("fd.file_id = ? AND fd.type LIKE ?", sourceFileID, "FILE_DERIVATIVE_TYPE_FAVICON_%").
		Order("fd.type ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	expected := outputSpecByType()
	if len(rows) != len(requiredOutputSpecs) {
		return nil, fmt.Errorf("favicon derivative set has %d rows, want %d", len(rows), len(requiredOutputSpecs))
	}
	refs, err := validateDerivativeRefs(db, cdnDomain, rows, expected)
	if err != nil {
		return nil, err
	}
	set, err := setFromRefs(refs)
	if err != nil {
		return nil, err
	}
	if normalizedMimeType(source.MimeType) == "image/svg+xml" {
		iconSVG, err := readySourceAssetRef(ctx, db, cdnDomain, sourceFileID)
		if err != nil {
			return nil, fmt.Errorf("SVG favicon source asset is not ready: %w", err)
		}
		if normalizedMimeType(iconSVG.GetMimeType()) != "image/svg+xml" || iconSVG.GetExtension() != "svg" {
			return nil, fmt.Errorf("SVG favicon source asset has invalid metadata")
		}
		set.IconSvg = iconSVG
	}
	return set, nil
}

func outputSpecByType() map[string]Spec {
	result := make(map[string]Spec, len(requiredOutputSpecs))
	for _, spec := range requiredOutputSpecs {
		result[spec.DerivativeType.String()] = spec
	}
	return result
}

func validateDerivativeRefs(
	db *gorm.DB,
	cdnDomain string,
	rows []derivativeAssetRow,
	expected map[string]Spec,
) (map[string]*commonv1.AssetRef, error) {
	lifecycle := mediaasset.NewLifecycle(db, cdnDomain)
	refs := make(map[string]*commonv1.AssetRef, len(rows))
	for _, row := range rows {
		spec, ok := expected[row.Type]
		if !ok {
			return nil, fmt.Errorf("favicon derivative set contains unexpected type %s", row.Type)
		}
		if _, duplicate := refs[row.Type]; duplicate {
			return nil, fmt.Errorf("favicon derivative set contains duplicate type %s", row.Type)
		}
		if !validDerivativeMetadata(row, spec) {
			return nil, fmt.Errorf("favicon derivative %s has invalid ready asset metadata", row.Type)
		}
		expectedKey, err := mediaauth.AssetObjectKey(row.ID, row.Extension)
		if err != nil || expectedKey != row.ObjectKey {
			return nil, fmt.Errorf("favicon derivative %s has a non-canonical object key", row.Type)
		}
		ref, err := lifecycle.AssetRef(model.PublicAsset{
			ID:               row.ID,
			Kind:             row.Kind,
			ObjectKey:        row.ObjectKey,
			Extension:        row.Extension,
			MimeType:         row.MimeType,
			FileSize:         row.FileSize,
			SHA256:           row.SHA256,
			Disposition:      row.Disposition,
			DownloadFilename: row.DownloadFilename,
			Status:           row.Status,
		})
		if err != nil {
			return nil, err
		}
		refs[row.Type] = ref
	}
	return refs, nil
}

func validDerivativeMetadata(row derivativeAssetRow, spec Spec) bool {
	return row.ID != "" && row.SourceFileID == nil && row.Kind == "favicon" && row.Extension == spec.Extension &&
		normalizedMimeType(row.MimeType) == spec.MimeType && row.FileSize != nil && *row.FileSize > 0 &&
		len(row.SHA256) == sha256.Size && row.Disposition == "inline" && row.DownloadFilename == nil &&
		row.Status == model.PublicAssetStatusReady
}

func setFromRefs(refs map[string]*commonv1.AssetRef) (*commonv1.FaviconAssetSet, error) {
	required := func(derivativeType managev1.FileDerivativeType) (*commonv1.AssetRef, error) {
		ref := refs[derivativeType.String()]
		if ref == nil {
			return nil, fmt.Errorf("favicon derivative set is missing %s", derivativeType.String())
		}
		return ref, nil
	}
	types := []managev1.FileDerivativeType{
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_ICO,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_16,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_32,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_48,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_APPLE_TOUCH_180,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_192,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_512,
	}
	resolved := make([]*commonv1.AssetRef, len(types))
	for index, derivativeType := range types {
		ref, err := required(derivativeType)
		if err != nil {
			return nil, err
		}
		resolved[index] = ref
	}
	return &commonv1.FaviconAssetSet{
		IconIco:            resolved[0],
		IconPng_16:         resolved[1],
		IconPng_32:         resolved[2],
		IconPng_48:         resolved[3],
		AppleTouchIcon_180: resolved[4],
		ManifestIcon_192:   resolved[5],
		ManifestIcon_512:   resolved[6],
	}, nil
}

// Projection selects generated PNG32 for the legacy browser field while
// returning the modern generated set. It falls back to the ready source asset.
func Projection(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	sourceFileID string,
) (*commonv1.AssetRef, *commonv1.FaviconAssetSet) {
	set, err := LoadSet(ctx, db, cdnDomain, sourceFileID)
	if err != nil {
		slog.Warn("invalid generated favicon derivative set", "file_id", sourceFileID, "error", err)
		return nil, nil
	}
	if set != nil {
		return set.GetIconPng_32(), set
	}
	legacy, err := readySourceAssetRef(ctx, db, cdnDomain, sourceFileID)
	if err != nil {
		slog.Warn("legacy favicon source asset not found", "file_id", sourceFileID, "error", err)
		return nil, nil
	}
	return legacy, nil
}

func readySourceAssetRef(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	sourceFileID string,
) (*commonv1.AssetRef, error) {
	var asset model.PublicAsset
	if err := db.WithContext(ctx).
		Where(
			"source_file_id = ? AND kind = ? AND status = ?",
			strings.TrimSpace(sourceFileID),
			"favicon",
			model.PublicAssetStatusReady,
		).
		Order("created_at DESC").
		Take(&asset).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("favicon_asset_for_source_file", sourceFileID)
		}
		return nil, errs.Internal(err)
	}
	return mediaasset.NewLifecycle(db, cdnDomain).AssetRef(asset)
}

// AssetIDs returns generated asset identities for cleanup preservation.
func AssetIDs(set *commonv1.FaviconAssetSet) map[string]struct{} {
	ids := make(map[string]struct{})
	if set == nil {
		return ids
	}
	assets := []*commonv1.AssetRef{
		set.GetIconIco(),
		set.GetIconPng_16(),
		set.GetIconPng_32(),
		set.GetIconPng_48(),
		set.GetAppleTouchIcon_180(),
		set.GetManifestIcon_192(),
		set.GetManifestIcon_512(),
		set.GetIconSvg(),
	}
	for _, asset := range assets {
		if asset != nil && strings.TrimSpace(asset.GetAssetId()) != "" {
			ids[asset.GetAssetId()] = struct{}{}
		}
	}
	return ids
}

func normalizedMimeType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
}

// RequestDeletion moves unbound favicon assets for a source File into the
// public-asset deletion lifecycle while preserving existing lock semantics.
func RequestDeletion(ctx context.Context, tx *gorm.DB, sourceFileID string) error {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if sourceFileID == "" {
		return nil
	}
	var derivativeCount int64
	if err := tx.WithContext(ctx).Table("file_derivative").
		Where("file_id = ? AND type LIKE ? AND asset_id IS NOT NULL", sourceFileID, "FILE_DERIVATIVE_TYPE_FAVICON_%").
		Count(&derivativeCount).Error; err != nil {
		return err
	}
	hasSourceAssetColumn := tx.Migrator().HasColumn(&model.PublicAsset{}, "SourceFileID")
	if derivativeCount == 0 {
		if !hasSourceAssetColumn {
			return nil
		}
		var sourceAssetCount int64
		if err := tx.WithContext(ctx).Model(&model.PublicAsset{}).
			Where("source_file_id = ? AND kind = ?", sourceFileID, "favicon").Count(&sourceAssetCount).Error; err != nil {
			return err
		}
		if sourceAssetCount == 0 {
			return nil
		}
	}
	derivativeAssetIDs := tx.Table("file_derivative").Select("asset_id").
		Where("file_id = ? AND type LIKE ? AND asset_id IS NOT NULL", sourceFileID, "FILE_DERIVATIVE_TYPE_FAVICON_%")
	var assets []model.PublicAsset
	assetQuery := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN (?)", derivativeAssetIDs)
	if hasSourceAssetColumn {
		assetQuery = assetQuery.Or("source_file_id = ? AND kind = ?", sourceFileID, "favicon")
	}
	if err := assetQuery.Order("id ASC").Find(&assets).Error; err != nil {
		return err
	}
	if len(assets) == 0 {
		return nil
	}
	assetIDs := make([]string, 0, len(assets))
	for _, asset := range assets {
		if asset.Kind != "favicon" {
			return errs.FailedPrecondition("favicon derivative references a non-favicon public asset")
		}
		assetIDs = append(assetIDs, asset.ID)
	}
	var bindingCount int64
	if err := tx.WithContext(ctx).
		Model(&model.PublicAssetBinding{}).
		Where("asset_id IN ?", assetIDs).
		Count(&bindingCount).Error; err != nil {
		return err
	}
	if bindingCount != 0 {
		return errs.FailedPrecondition("favicon assets still have active bindings")
	}
	lifecycle := mediaasset.NewLifecycle(tx, "")
	for _, asset := range assets {
		switch asset.Status {
		case model.PublicAssetStatusReady, model.PublicAssetStatusFailed:
			if err := lifecycle.RequestPublicAssetDeletion(ctx, asset.ID); err != nil {
				return err
			}
		case model.PublicAssetStatusDeletePending, model.PublicAssetStatusDeleted:
			continue
		default:
			return errs.FailedPrecondition("favicon asset cannot be deleted from its current state")
		}
	}
	return nil
}
