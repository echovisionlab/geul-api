// Package pagepostgres is the PostgreSQL page.Store: the page tables, and
// the shared content, media, OG, route, Member, Share Link and Menu runtimes
// a Page transaction drives, bound to one gorm transaction.
package pagepostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/identitystate"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	pageaccesspostgres "github.com/echovisionlab/geul-api/internal/pageaccess/postgres"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// OGRequests is the narrow OG lifecycle a Page transaction drives.
type OGRequests interface {
	RequestCurrentWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string, bool, string) (string, error)
	CancelAndReleaseEntityWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string) error
}

// MediaAssets is the narrow File/media projection lifecycle a Page
// transaction drives.
type MediaAssets interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ReleaseExactPublicAssetBindings(context.Context, *gorm.DB, string, string, []string) error
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*mediaasset.Reference, error)
	LoadUnavailableVersionAttachmentKinds(context.Context, *gorm.DB, proto.Message) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
}

// Runtime is the shared OG and media lifecycle of a Page; the Page runtime
// adapter implements it.
type Runtime interface {
	OGRequests
	MediaAssets
}

// ContentBlockMediaHydrator adds request-scoped delivery to authorized Page
// Block media references.
type ContentBlockMediaHydrator interface {
	HydrateAuthorizedPageBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*fileref.BlockMediaItem,
	) ([]*fileref.BlockMediaItem, error)
}

// Members reads the Member and user tag rows Page authorization and
// attribution need; Member owns them.
type Members interface {
	LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error)
	ActiveIdentities(ctx context.Context, tx *gorm.DB, memberIDs []string) (map[string]string, error)
	Nicknames(ctx context.Context, db *gorm.DB, memberIDs []string) (map[string]string, error)
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
	LockUserTags(ctx context.Context, tx *gorm.DB, tagIDs []string) ([]string, error)
}

// ShareLinks removes a deleted Page's share links in its transaction;
// Share Link owns them.
type ShareLinks interface {
	DeletePageShareLinks(ctx context.Context, tx *gorm.DB, pageID string) error
}

// MenuTargets owns cross-domain Menu reference rewrites triggered by Page
// slug changes and deletion.
type MenuTargets interface {
	UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error
	Remove(context.Context, *gorm.DB, string, string, string) error
}

// Ports are the other domains and shared runtimes the Store drives in the
// Page transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	Signals       contentsignal.Publisher
	Runtime       Runtime
	MediaHydrator ContentBlockMediaHydrator
	MenuTargets   MenuTargets
	Members       Members
	ShareLinks    ShareLinks
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "Signals": p.Signals == nil, "Runtime": p.Runtime == nil,
		"MediaHydrator": p.MediaHydrator == nil, "MenuTargets": p.MenuTargets == nil, "Members": p.Members == nil,
		"ShareLinks": p.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("page store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Page use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[page.Store], error) {
	if db == nil {
		return nil, errors.New("page store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) page.Store { return NewStore(tx, ports) }), nil
}

// NewStore binds the Store to tx. Only the methods whose ports ports names
// are usable, so a port another domain drives in its own transaction binds
// what it needs.
func NewStore(tx *gorm.DB, ports Ports) page.Store {
	return &store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ page.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) page.Store {
	return NewStore(tx, s.ports)
}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers a domain sentinel.
type notFoundError struct {
	error
	sentinel error
}

func (err notFoundError) Is(target error) bool { return target == err.sentinel }
func (err notFoundError) Unwrap() error        { return err.error }

func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}

func (s *store) LockActivePrincipal(ctx context.Context, principal *auth.UserInfo) (bool, error) {
	return pageaccesspostgres.NewAudience(s.tx, identitystate.PostgresFence{}).LockActivePrincipal(ctx, principal)
}

func (s *store) HasUserTag(ctx context.Context, memberID string, tagIDs []string) (bool, error) {
	return pageaccesspostgres.NewAudience(s.tx, identitystate.PostgresFence{}).HasUserTag(ctx, memberID, tagIDs)
}

func (s *store) SubscribesToNewsletter(ctx context.Context, identityID string) (bool, error) {
	return pageaccesspostgres.NewAudience(s.tx, identitystate.PostgresFence{}).SubscribesToNewsletter(ctx, identityID)
}
