package form

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// AsyncPublisher is the command and signal surface used by Form translation planning.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

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

// SecurityAccess records reads of Form submission personal data.
type SecurityAccess interface {
	AppendFormSubmissions(context.Context, string) error
	AppendFormSubmission(context.Context, string) error
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

// Translation owns Form's root-locale defaults and collaboration fence used by Forms.
type Translation interface {
	ResolveInitialSourceLocale(context.Context, *gorm.DB, auth.IdentityManager, string) string
	NormalizeInitialSourceLocale(context.Context, *gorm.DB, string) string
	DefaultLocale(context.Context, *gorm.DB) string
	LockRoot(context.Context, *gorm.DB, string) error
	RequireMutationContributor(
		context.Context,
		*gorm.DB,
		*auth.SpiceDBClient,
		intrav1.CollaborationResourceType,
		string,
		string,
	) error
}

type Dependencies struct {
	ContentBlocks  *contentblock.Store
	Assets         Assets
	OG             OG
	Routes         Routes
	SecurityAccess SecurityAccess
	Translation    Translation
	PublicAssets   PublicAssets
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
