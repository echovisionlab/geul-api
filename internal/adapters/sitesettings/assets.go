package sitesettings

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	domain "github.com/echovisionlab/geul-api/internal/sitesettings"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	siteSettingsAssetOwnerType = "site_settings"
	siteSettingsAssetOwnerID   = "1"
)

type faviconBinding struct {
	key   string
	asset *commonv1.AssetRef
}

// Assets adapts shared public-asset lifecycle operations to Site Settings.
type Assets struct {
	cdnDomain string
}

var _ domain.Assets = Assets{}

func NewAssets(cdnDomain string) Assets {
	return Assets{cdnDomain: cdnDomain}
}

func (Assets) ValidateAttachment(ctx context.Context, db *gorm.DB, key, fileID string) error {
	uploadType, managed := siteSettingAssetUploadType(key)
	if !managed {
		return nil
	}
	config := model.DefaultUploadConfigs[uploadType]
	if config == nil {
		return errs.Internal(fmt.Errorf("site setting upload config is missing for %s", key))
	}
	var file model.File
	if err := db.WithContext(ctx).Select("id", "mime_type", "file_size").
		Where("id = ?", strings.TrimSpace(fileID)).Take(&file).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.FailedPrecondition("file no longer exists")
		}
		return errs.Internal(err)
	}
	mimeType := normalizeSiteSettingMIME(file.MimeType)
	allowed := make(map[string]struct{}, len(config.PermittedMimeTypes))
	for _, allowedMIME := range config.PermittedMimeTypes {
		if normalized := normalizeSiteSettingMIME(allowedMIME); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	if _, ok := allowed[mimeType]; !ok {
		return errs.InvalidArgument("file_id", fmt.Sprintf("%s does not accept %s", key, mimeType))
	}
	if key == "logo_email_file_id" && mimeType != "image/png" {
		return errs.InvalidArgument("file_id", "logo_email_file_id requires image/png")
	}
	if file.FileSize < config.MinSize || file.FileSize > config.MaxSize {
		return errs.InvalidArgument("file_id", fmt.Sprintf("%s file size is outside the supported range", key))
	}
	return nil
}

func siteSettingAssetUploadType(key string) (managev1.UploadType, bool) {
	switch key {
	case "logo_light_file_id", "logo_dark_file_id", "logo_email_file_id":
		return managev1.UploadType_UPLOAD_TYPE_SITE_LOGO, true
	case "favicon_file_id":
		return managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON, true
	case "site_og_background_file_id", "privacy_og_background_file_id", "terms_og_background_file_id":
		return managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND, true
	case "loader":
		return managev1.UploadType_UPLOAD_TYPE_SITE_LOADER, true
	default:
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED, false
	}
}

func normalizeSiteSettingMIME(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
}

func (a Assets) LockForAttachment(ctx context.Context, db *gorm.DB, fileIDs []string) error {
	return mediaasset.LockAttachableFilesForUpdate(ctx, db, fileIDs)
}

func (a Assets) BindReady(
	ctx context.Context,
	db *gorm.DB,
	binding domain.AssetBinding,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(db, a.cdnDomain).BindReadyAssetForSourceFile(
		ctx,
		binding.SourceFileID,
		siteSettingsAssetOwnerType,
		siteSettingsAssetOwnerID,
		binding.Key,
		binding.Kind,
	)
}

func (a Assets) Release(ctx context.Context, db *gorm.DB, bindingPrefix string) error {
	return mediaasset.NewLifecycle(db, a.cdnDomain).ReleasePublicAssetBindings(
		ctx,
		siteSettingsAssetOwnerType,
		siteSettingsAssetOwnerID,
		bindingPrefix,
	)
}

func (a Assets) ReplaceFavicon(ctx context.Context, db *gorm.DB, fileID *string) error {
	lifecycle := mediaasset.NewLifecycle(db, a.cdnDomain)
	if err := a.Release(ctx, db, "favicon"); err != nil {
		return err
	}
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	set, err := favicon.LoadSet(ctx, db, a.cdnDomain, *fileID)
	if err != nil {
		return err
	}
	if set == nil {
		_, err := a.BindReady(ctx, db, domain.AssetBinding{
			SourceFileID: *fileID,
			Key:          "favicon",
			Kind:         "favicon",
		})
		return err
	}
	for _, binding := range siteSettingsFaviconBindings(set) {
		if binding.asset == nil || strings.TrimSpace(binding.asset.GetAssetId()) == "" {
			return errs.FailedPrecondition("generated favicon bundle is missing " + binding.key)
		}
		if err := lifecycle.BindPublicAsset(ctx, mediaasset.Binding{
			AssetID:      binding.asset.GetAssetId(),
			OwnerType:    siteSettingsAssetOwnerType,
			OwnerID:      siteSettingsAssetOwnerID,
			BindingKey:   binding.key,
			SourceFileID: fileID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func siteSettingsFaviconBindings(set *commonv1.FaviconAssetSet) []faviconBinding {
	bindings := []faviconBinding{
		{key: "favicon", asset: set.GetIconPng_32()},
		{key: "favicon:ico", asset: set.GetIconIco()},
		{key: "favicon:png16", asset: set.GetIconPng_16()},
		{key: "favicon:png48", asset: set.GetIconPng_48()},
		{key: "favicon:apple180", asset: set.GetAppleTouchIcon_180()},
		{key: "favicon:manifest192", asset: set.GetManifestIcon_192()},
		{key: "favicon:manifest512", asset: set.GetManifestIcon_512()},
	}
	if set.GetIconSvg() != nil {
		bindings = append(bindings, faviconBinding{key: "favicon:svg", asset: set.GetIconSvg()})
	}
	return bindings
}

func (a Assets) ReadyRef(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
	kind string,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(db, a.cdnDomain).ReadyAssetRefForSourceFile(ctx, fileID, kind)
}

func (a Assets) ReadyAsset(
	ctx context.Context,
	db *gorm.DB,
	assetID string,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(db, a.cdnDomain).ReadyAssetRef(ctx, assetID)
}

func (a Assets) ProjectFavicon(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (*commonv1.AssetRef, *commonv1.FaviconAssetSet) {
	return favicon.Projection(ctx, db, a.cdnDomain, fileID)
}
