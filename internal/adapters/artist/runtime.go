package artist

import (
	"context"
	"strings"

	"gorm.io/gorm"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Runtime adapts shared persistence, media, OG, and route capabilities to the
// narrow surface owned by Artist.
type Runtime struct {
	cdnDomain string
	refresher *og.Refresher
}

func NewRuntime(cdnDomain string, refresher *og.Refresher) *Runtime {
	if refresher == nil {
		panic("Artist runtime OG refresher is required")
	}
	return &Runtime{
		cdnDomain: strings.TrimRight(strings.TrimSpace(cdnDomain), "/"),
		refresher: refresher,
	}
}

func (r *Runtime) LoadArtistImages(
	ctx context.Context,
	db *gorm.DB,
	artistIDs []string,
) (map[string][]artistdomain.ArtistImageProjection, error) {
	result := make(map[string][]artistdomain.ArtistImageProjection, len(artistIDs))
	artistIDs = normalizedUniqueStrings(artistIDs)
	if len(artistIDs) == 0 {
		return result, nil
	}

	var rows []struct {
		ArtistID    string  `gorm:"column:artist_id"`
		FileID      string  `gorm:"column:file_id"`
		SortOrder   int32   `gorm:"column:sort_order"`
		AssetID     *string `gorm:"column:asset_id"`
		Extension   *string `gorm:"column:extension"`
		MimeType    *string `gorm:"column:mime_type"`
		FileSize    *int64  `gorm:"column:file_size"`
		SHA256      []byte  `gorm:"column:sha256"`
		Disposition *string `gorm:"column:disposition"`
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT relation.artist_id::text,
		       relation.file_id::text,
		       relation.sort_order,
		       asset.id::text AS asset_id,
		       asset.extension,
		       asset.mime_type,
		       asset.file_size,
		       asset.sha256,
		       asset.disposition
		FROM artist_file AS relation
		LEFT JOIN LATERAL (
			SELECT public_asset.id,
			       public_asset.extension,
			       public_asset.mime_type,
			       public_asset.file_size,
			       public_asset.sha256,
			       public_asset.disposition
			FROM public_asset
			WHERE public_asset.source_file_id = relation.file_id
			  AND public_asset.kind = 'image'
			  AND public_asset.status = 'ready'
			ORDER BY public_asset.created_at DESC, public_asset.id DESC
			LIMIT 1
		) AS asset ON TRUE
		WHERE relation.artist_id IN ?
		ORDER BY relation.artist_id, relation.sort_order, relation.file_id
	`, artistIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}

	lifecycle := mediaasset.NewLifecycle(db, r.cdnDomain)
	for _, row := range rows {
		projection := artistdomain.ArtistImageProjection{
			ArtistID: row.ArtistID, FileID: row.FileID, SortOrder: row.SortOrder,
		}
		if row.AssetID != nil && row.Extension != nil && row.MimeType != nil && row.FileSize != nil && row.Disposition != nil {
			asset, err := lifecycle.AssetRef(model.PublicAsset{
				ID: *row.AssetID, Kind: "image", Extension: *row.Extension,
				MimeType: *row.MimeType, FileSize: row.FileSize, SHA256: row.SHA256,
				Disposition: *row.Disposition,
			})
			if err != nil {
				return nil, err
			}
			projection.Asset = asset
		}
		result[row.ArtistID] = append(result[row.ArtistID], projection)
	}
	return result, nil
}

func (r *Runtime) ResolveReadyAssetRefs(
	ctx context.Context,
	db *gorm.DB,
	assetIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	assetIDs = normalizedUniqueStrings(assetIDs)
	result := make(map[string]*commonv1.AssetRef, len(assetIDs))
	if len(assetIDs) == 0 {
		return result, nil
	}
	var assets []model.PublicAsset
	if err := db.WithContext(ctx).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(db, r.cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		result[asset.ID] = ref
	}
	return result, nil
}

func (r *Runtime) ReplaceArtistImageBindings(
	ctx context.Context,
	tx *gorm.DB,
	artistID string,
	fileIDs []string,
) error {
	if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, fileIDs); err != nil {
		return err
	}
	var currentKeys []string
	if err := tx.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ?", "artist", artistID).
		Where("binding_key = ? OR binding_key LIKE ?", "image", "image:%").
		Order("binding_key ASC").Pluck("binding_key", &currentKeys).Error; err != nil {
		return err
	}
	if err := tx.Where("artist_id = ?", artistID).Delete(&model.ArtistFile{}).Error; err != nil {
		return err
	}
	lifecycle := mediaasset.NewLifecycle(tx, r.cdnDomain)
	nextKeys := make(map[string]struct{}, len(fileIDs))
	for index, fileID := range fileIDs {
		if err := tx.Create(&model.ArtistFile{ArtistID: artistID, FileID: fileID, SortOrder: index}).Error; err != nil {
			return err
		}
		asset, err := lifecycle.ReadyAssetRefForSourceFile(ctx, fileID, "image")
		if err != nil {
			return err
		}
		key := "image:" + fileID
		nextKeys[key] = struct{}{}
		if err := lifecycle.BindPublicAsset(ctx, mediaasset.Binding{
			AssetID: asset.GetAssetId(), OwnerType: "artist", OwnerID: artistID,
			BindingKey: key, SourceFileID: &fileID,
		}); err != nil {
			return err
		}
	}
	removed := make([]string, 0, len(currentKeys))
	for _, key := range currentKeys {
		if _, retained := nextKeys[key]; !retained {
			removed = append(removed, key)
		}
	}
	return lifecycle.ReleaseExactPublicAssetBindings(ctx, "artist", artistID, removed)
}

func (r *Runtime) RequestCurrentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	entityID string,
	locale string,
	allLocales bool,
	reason string,
) (string, error) {
	plan, err := r.refresher.RequestCurrentWithDB(ctx, tx, entityType, entityID, locale, allLocales, reason)
	if err != nil || plan == nil {
		return "", err
	}
	return plan.RunID, nil
}

func (r *Runtime) CancelAndReleaseEntityWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	ownerType string,
	ownerID string,
) error {
	if err := og.NewLifecycle(tx, r.cdnDomain).CancelEntityWithDB(ctx, tx, entityType, ownerID); err != nil {
		return err
	}
	return r.ReleasePublicAssetBindings(ctx, tx, ownerType, ownerID, "og")
}

func (r *Runtime) ReleasePublicAssetBindings(
	ctx context.Context,
	tx *gorm.DB,
	ownerType string,
	ownerID string,
	bindingPrefix string,
) error {
	return mediaasset.NewLifecycle(tx, r.cdnDomain).
		ReleasePublicAssetBindings(ctx, ownerType, ownerID, bindingPrefix)
}

func (*Runtime) EnsureResourceRouteAvailable(
	ctx context.Context,
	tx *gorm.DB,
	resourceType string,
	pathPrefix string,
	slug string,
) error {
	return routeregistry.EnsureResourceRouteAvailable(ctx, tx, resourceType, pathPrefix, slug)
}

func (*Runtime) EnsureResourceRouteAvailableInTx(
	ctx context.Context,
	tx *gorm.DB,
	resourceType string,
	pathPrefix string,
	slug string,
) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, resourceType, pathPrefix, slug)
}

func (*Runtime) IsResourceRouteAvailable(
	ctx context.Context,
	db *gorm.DB,
	pathPrefix string,
	slug string,
) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, db, pathPrefix, slug)
}

func normalizedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

var _ artistdomain.Runtime = (*Runtime)(nil)
