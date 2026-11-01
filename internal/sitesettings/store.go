package sitesettings

import (
	"context"
	"slices"
	"time"
)

// Settings are the stored values of the singleton settings. The JSON
// settings stay encoded, because reads serve an undecodable stored value as
// its empty fallback while writes reject it.
type Settings struct {
	SiteTitle      string
	CompanyName    string
	CompanyAddress string
	TaxID          string
	ContactEmail   string
	LegalEmail     string
	SocialLinks    []byte

	LogoFullLightFileID       *string
	LogoFullDarkFileID        *string
	LogoSymbolLightFileID     *string
	LogoSymbolDarkFileID      *string
	LogoEmailLightFileID      *string
	LogoEmailDarkFileID       *string
	FaviconFileID             *string
	PrimaryColor              string
	SiteOgBackgroundFileID    *string
	PrivacyOgBackgroundFileID *string
	TermsOgBackgroundFileID   *string

	DefaultCommentsEnabled bool
	HomepagePageID         *string
	DefaultMapThemeID      string

	MenuHeaderID         *string
	MenuSecondaryID      *string
	MenuFooterID         *string
	MenuAvatarDropdownID *string

	MetaDescription   string
	GoogleAnalyticsID *string
	// SiteOgAssetID is the generated site OG image; OG generation owns it.
	SiteOgAssetID *string

	OGImageConfig []byte

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Clone copies settings, its encoded JSON values included.
func (settings Settings) Clone() Settings {
	settings.SocialLinks = slices.Clone(settings.SocialLinks)
	settings.OGImageConfig = slices.Clone(settings.OGImageConfig)
	return settings
}

// AssetDisposition is the content disposition of a public asset, numbered as
// the API's AssetDisposition.
type AssetDisposition int32

// Asset is a ready public asset as the media runtime projects it.
type Asset struct {
	ID               string
	URL              string
	Extension        string
	MIMEType         string
	FileSize         int64
	SHA256           []byte
	Disposition      AssetDisposition
	DownloadFilename *string
}

// FaviconSet is the generated favicon bundle of a favicon File.
type FaviconSet struct {
	ICO           *Asset
	PNG16         *Asset
	PNG32         *Asset
	PNG48         *Asset
	AppleTouch180 *Asset
	Manifest192   *Asset
	Manifest512   *Asset
	SVG           *Asset
}

// AssetBinding identifies a public asset usage owned by Site Settings.
type AssetBinding struct {
	SourceFileID string
	Key          string
	Kind         string
}

// Store is what the Site Settings use cases read and write, bound to one
// transaction or read snapshot. Besides the settings row and its loader
// Files it drives, in the same transaction, the File and public asset
// lifecycle, the Page and Menu checks of the reference settings, and OG
// regeneration planning, whose answers pass through unchanged.
type Store interface {
	// Settings reads the settings.
	Settings(ctx context.Context) (Settings, error)
	// LockSettings locks the settings for update and reads them.
	LockSettings(ctx context.Context) (Settings, error)
	// SaveSettings stores every value of settings.
	SaveSettings(ctx context.Context, settings Settings) error
	// TouchSettings stamps the settings as updated now.
	TouchSettings(ctx context.Context) error
	// LoaderFileIDs lists the site loader Files by position, then by when
	// they were added.
	LoaderFileIDs(ctx context.Context) ([]string, error)
	// AddLoaderFile appends fileID to the loader Files; false when it is one
	// already.
	AddLoaderFile(ctx context.Context, fileID string) (bool, error)
	// RemoveLoaderFile removes fileID from the loader Files; false when it is
	// none of them.
	RemoveLoaderFile(ctx context.Context, fileID string) (bool, error)

	// LockFilesForAttachment locks the Files a change attaches.
	LockFilesForAttachment(ctx context.Context, fileIDs []string) error
	// ValidateAttachment checks that File fileID suits setting key, or the
	// site loader when key is "loader".
	ValidateAttachment(ctx context.Context, key, fileID string) error
	// BindAsset binds the ready asset of binding's File under binding's key.
	BindAsset(ctx context.Context, binding AssetBinding) error
	// ReleaseAssets releases the asset bindings whose key starts with prefix.
	ReleaseAssets(ctx context.Context, prefix string) error
	// ReplaceFavicon releases the favicon bindings and binds the favicon of
	// File fileID, or none when fileID is nil or blank.
	ReplaceFavicon(ctx context.Context, fileID *string) error
	// ReadyAsset reads the ready asset of File fileID for kind.
	ReadyAsset(ctx context.Context, fileID, kind string) (*Asset, error)
	// Favicon reads the favicon of File fileID and its generated set. It
	// logs a failure and answers none.
	Favicon(ctx context.Context, fileID string) (*Asset, *FaviconSet)

	// ValidateReference checks the Page or Menu that setting key selects.
	ValidateReference(ctx context.Context, key string, id *string) error
	// RequestOGRegeneration plans the OG regeneration that changing before
	// into after by changedKeys requires, and returns its run, if any.
	RequestOGRegeneration(ctx context.Context, before, after Settings, changedKeys []string) (runID *string, err error)
}
