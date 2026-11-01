package public

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
)

var ErrAssetNotFound = errors.New("ready asset not found")

type ReadyAsset struct {
	AssetID                   string
	SourceFileID              *string
	Kind, Extension, MimeType string
	FileSize                  int64
	SHA256                    []byte
	Disposition               string
	DownloadFilename          *string
}
type ReadyGeneration struct{ FileID, GenerationID, ManifestName string }
type MediaFile struct {
	ID, Extension, MimeType string
	FileSize                int64
	FileName                *string
	DurationSeconds         *int
}
type Derivative struct {
	FileID, Type               string
	AssetID, MediaGenerationID *string
}
type OwnerWitness struct{ ResourceType, ResourceID, Status, DocumentID string }
type ShareLink struct {
	ID, EntityType, EntityID string
	ExpiresAt                *time.Time
}

// Store joins all download and delivery reads to one snapshot or locking pass.
type Store interface {
	mediaasset.DownloadStore
	ReadyAsset(context.Context, string) (ReadyAsset, error)
	ReadyAssetsForFiles(context.Context, []string, ...string) (map[string]ReadyAsset, error)
	ReadyAssets(context.Context, []string) (map[string]ReadyAsset, error)
	ReadyGenerations(context.Context, []string) (map[string]ReadyGeneration, error)
	MediaFiles(context.Context, []string) ([]MediaFile, error)
	Derivatives(context.Context, []string) ([]Derivative, error)
	LockContentOwner(context.Context, MediaEntityType, string) (string, bool, error)
	LockReleaseOwner(context.Context, string) (string, bool, error)
	ResolveContentSource(context.Context, MediaEntityType, string, string, string, bool) (mediaasset.FileDownloadSource, string, bool, error)
	ResolveTrackSource(context.Context, string, string, bool) (mediaasset.FileDownloadSource, string, bool, error)
	PageAccess(context.Context, authz.AuthorizationDecisionChecker, string) (pageaccess.Reason, error)
	LockActivePrincipal(context.Context, *auth.UserInfo) (bool, error)
	ShareLink(context.Context, string, bool) (ShareLink, bool, error)
	ContentOwners(context.Context, []mediaasset.ContentBlockDownloadSelector) (map[string]OwnerWitness, error)
	LockContentOwners(context.Context, map[string]OwnerWitness) (map[string]OwnerWitness, error)
	ContentRelations(context.Context, []mediaasset.ContentBlockDownloadSelector, bool) (map[string]string, error)
}
