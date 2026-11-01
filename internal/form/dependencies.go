package form

import (
	"context"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"gorm.io/gorm"
)

// Assets owns Form integration with File attachment and public asset bindings.
type Assets interface {
	LockAttachableFiles(context.Context, *gorm.DB, []string) error
	BindFeaturedImage(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	ReleaseFeaturedImage(context.Context, *gorm.DB, string) error
	FeaturedImage(context.Context, *gorm.DB, string) *commonv1.AssetRef
}

// OG owns Form integration with generated Open Graph assets.
type OG interface {
	Request(context.Context, *gorm.DB, string, string, string, bool, string) (string, error)
	RequestAfterMutation(context.Context, *gorm.DB, string, string, bool, string) (*string, error)
	CancelAndRelease(context.Context, *gorm.DB, string) error
	BaseTitle(context.Context, *gorm.DB, string) (string, error)
	ReadyAsset(context.Context, *gorm.DB, *string, *string) (*commonv1.AssetRef, error)
}

// Routes owns collision checks between Form slugs and Page routes.
type Routes interface {
	SlugAvailable(context.Context, *gorm.DB, string, string) (bool, error)
	EnsureAvailable(context.Context, *gorm.DB, string, string) error
	EnsureAvailableLocked(context.Context, *gorm.DB, string, string) error
}

// LocalizedOGDisposition is the Form-owned public-read view of a localized OG generation.
type LocalizedOGDisposition uint8

const (
	LocalizedOGUnavailable LocalizedOGDisposition = iota
	LocalizedOGPending
	LocalizedOGReady
)

// PublicAssets projects Form-owned File and OG references for public reads.
type PublicAssets interface {
	LocalizedOGDisposition(context.Context, *gorm.DB, string, string) (LocalizedOGDisposition, error)
	ResolvedOG(context.Context, *gorm.DB, *string, *string) (*commonv1.AssetRef, error)
	FeaturedImage(context.Context, *gorm.DB, string) *commonv1.AssetRef
}

type LocalizationSelection struct {
	RequestedLocale      string
	DisplayedLocale      string
	SourceLocale         string
	AvailableLocales     []string
	IsFallback           bool
	IsOriginal           bool
	FallbackReason       openv1.LocalizationFallbackReason
	Title                *string
	Summary              *string
	ContentJSON          []byte
	ContentHTML          *string
	ContentText          *string
	OgAssetID            *string
	OmitSourceOgFallback bool
}

// Collaboration re-resolves the one origin Member a collaboration save is
// attributed to, inside the save's transaction.
type Collaboration interface {
	RequireCurrentContributor(ctx context.Context, tx *gorm.DB, formID, memberID string) error
}

// Members reads the Member rows Forms need; Member owns them.
type Members interface {
	// PreferredLocale returns the preferred locale of a live Member, or nil.
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
}

// ShareLinks removes a deleted Form's share links in its transaction; Share
// Link owns them.
type ShareLinks interface {
	DeleteFormShareLinks(ctx context.Context, tx *gorm.DB, formID string) error
}

type TranslationDocumentState struct {
	Title       *string
	ContentJSON []byte
	ContentText *string
	OgAssetID   *string
}

type TranslationDocumentSaveInput struct {
	Title       *string
	ContentJSON []byte
	Now         time.Time
}
