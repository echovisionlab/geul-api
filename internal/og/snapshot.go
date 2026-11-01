package og

import "encoding/json"

type ogAssetRefSnapshot struct {
	AssetID   string `json:"asset_id"`
	URL       string `json:"url"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

type ogOutputSnapshot struct {
	AssetID   string `json:"asset_id"`
	ObjectKey string `json:"object_key"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

type ogEntitySnapshot struct {
	EntityType    string              `json:"entity_type"`
	EntityID      string              `json:"entity_id"`
	Title         string              `json:"title"`
	Locale        *string             `json:"locale,omitempty"`
	FeaturedImage *ogAssetRefSnapshot `json:"featured_image,omitempty"`
	Output        ogOutputSnapshot    `json:"output"`
}

type ogRenderConfigSnapshot struct {
	SiteTitle     string              `json:"site_title"`
	PrimaryColor  string              `json:"primary_color"`
	LogoAsset     *ogAssetRefSnapshot `json:"logo_asset,omitempty"`
	OGImageConfig json.RawMessage     `json:"og_image_config,omitempty"`
}

func snapshotAssetRef(asset AssetRef) *ogAssetRefSnapshot {
	return &ogAssetRefSnapshot{
		AssetID:   asset.AssetID,
		URL:       asset.URL,
		Extension: asset.Extension,
		MimeType:  asset.MimeType,
	}
}
