package sitesettings

import (
	"context"
	"errors"
	"fmt"
	"strings"

	optional "github.com/echovisionlab/geul-api/internal/optional"

	"github.com/echovisionlab/geul-api/internal/favicon"
	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia/domain"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	faviconpostgres "github.com/echovisionlab/geul-api/internal/favicon/postgres"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"

	domain "github.com/echovisionlab/geul-api/internal/sitesettings"

	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	siteSettingsAssetOwnerType = "site_settings"
	siteSettingsAssetOwnerID   = "1"
)

type faviconBinding struct {
	key   string
	asset *mediaasset.Reference
}

// Assets adapts shared public-asset lifecycle operations to Site Settings.
type Assets struct {
	cdnDomain string
}

var _ sitesettingspostgres.Assets = Assets{}

func NewAssets(cdnDomain string) Assets {
	return Assets{cdnDomain: cdnDomain}
}

func (Assets) ValidateAttachment(ctx context.Context, db *gorm.DB, key, fileID string) error {
	uploadType, managed := siteSettingAssetUploadType(key)
	if !managed {
		return nil
	}
	config := filemediadomain.DefaultUploadConfigs[uploadType.String()]
	if config == nil {
		return errs.Internal(fmt.Errorf("site setting upload config is missing for %s", key))
	}
	var file model.File
	if err := db.WithContext(ctx).Select("id", "mime_type", "file_size").
		Where("id = ?", strings.TrimSpace(fileID)).Take(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
	if (key == "logo_email_light_file_id" || key == "logo_email_dark_file_id") && mimeType != "image/png" {
		return errs.InvalidArgument("file_id", key+" requires image/png")
	}
	if file.FileSize < config.MinSize || file.FileSize > config.MaxSize {
		return errs.InvalidArgument("file_id", fmt.Sprintf("%s file size is outside the supported range", key))
	}
	return nil
}

func siteSettingAssetUploadType(key string) (managev1.UploadType, bool) {
	switch key {
	case "logo_full_light_file_id", "logo_full_dark_file_id",
		"logo_symbol_light_file_id", "logo_symbol_dark_file_id",
		"logo_email_light_file_id", "logo_email_dark_file_id":
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
	return mediaassetpostgres.NewStore(db).LockAttachableFiles(ctx, fileIDs)
}

func (a Assets) BindReady(
	ctx context.Context,
	db *gorm.DB,
	binding domain.AssetBinding,
) (*mediaasset.Reference, error) {
	_, kind, err := readySiteSettingAsset(ctx, db, a.cdnDomain, binding.SourceFileID, binding.Kind)
	if err != nil {
		return nil, err
	}
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), a.cdnDomain).BindReadyAssetForSourceFile(
		ctx,
		binding.SourceFileID,
		siteSettingsAssetOwnerType,
		siteSettingsAssetOwnerID,
		binding.Key,
		kind,
	)
}

func (a Assets) Release(ctx context.Context, db *gorm.DB, bindingPrefix string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), a.cdnDomain).ReleasePublicAssetBindings(
		ctx,
		siteSettingsAssetOwnerType,
		siteSettingsAssetOwnerID,
		bindingPrefix,
	)
}

func (a Assets) ReplaceFavicon(ctx context.Context, db *gorm.DB, fileID *string) error {
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), a.cdnDomain)
	if err := a.Release(ctx, db, "favicon"); err != nil {
		return err
	}
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	set, err := faviconpostgres.LoadSet(ctx, db, a.cdnDomain, *fileID)
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
		if binding.asset == nil || strings.TrimSpace(optional.Value(binding.asset).AssetID) == "" {
			return errs.FailedPrecondition("generated favicon bundle is missing " + binding.key)
		}
		if err := lifecycle.BindPublicAsset(ctx, mediaasset.Binding{
			AssetID:      optional.Value(binding.asset).AssetID,
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

func siteSettingsFaviconBindings(set *favicon.Set) []faviconBinding {
	bindings := []faviconBinding{
		{key: "favicon", asset: set.IconPNG32},
		{key: "favicon_ico", asset: set.IconICO},
		{key: "favicon_png16", asset: set.IconPNG16},
		{key: "favicon_png48", asset: set.IconPNG48},
		{key: "favicon_apple180", asset: set.AppleTouch180},
		{key: "favicon_manifest192", asset: set.Manifest192},
		{key: "favicon_manifest512", asset: set.Manifest512},
	}
	if set.IconSVG != nil {
		bindings = append(bindings, faviconBinding{key: "favicon_svg", asset: set.IconSVG})
	}
	return bindings
}

func (a Assets) ReadyRef(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
	kind string,
) (*mediaasset.Reference, error) {
	ref, _, err := readySiteSettingAsset(ctx, db, a.cdnDomain, fileID, kind)
	return ref, err
}

func readySiteSettingAsset(ctx context.Context, db *gorm.DB, cdnDomain, fileID, kind string) (*mediaasset.Reference, string, error) {
	return mediaasset.NewReferenceReader(mediaassetpostgres.NewStore(db), cdnDomain).ReadyBrandingAssetRef(ctx, fileID, kind)
}

func (a Assets) ReadyAsset(
	ctx context.Context,
	db *gorm.DB,
	assetID string,
) (*mediaasset.Reference, error) {
	return mediaasset.NewReferenceReader(mediaassetpostgres.NewStore(db), a.cdnDomain).ReadyAssetRef(ctx, assetID)
}

func (a Assets) ProjectFavicon(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (*mediaasset.Reference, *favicon.Set) {
	ref, set := faviconpostgres.Projection(ctx, db, a.cdnDomain, fileID)
	return ref, set
}
