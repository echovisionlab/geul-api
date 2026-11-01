// Package postpostgres is the PostgreSQL post.Store: the post tables, and
// the shared content, media, OG, route and translation runtimes a Post
// transaction drives, bound to one gorm transaction.
package postpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/mutation"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/post"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// ShareLinkCleanup removes the share links of a Post in the deleting
// transaction. Share Link owns the rows.
type ShareLinkCleanup interface {
	DeleteEntityShareLinks(ctx context.Context, tx *gorm.DB, postID string) error
}

// ContentBlockMediaLoader loads persisted attachment references only after a
// Post operation has authorized the document.
type ContentBlockMediaLoader interface {
	LoadContentBlockMediaReferences(context.Context, *gorm.DB, uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error)
}

// ContentBlockMediaHydrator adds private delivery while Post holds the
// authorized owner, document, and attachment witnesses in one transaction.
type ContentBlockMediaHydrator interface {
	HydrateAuthorizedPostBlockMediaWithDB(context.Context, *gorm.DB, string, uuid.UUID, *auth.UserInfo, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
}

// VersionRestoreSupport reads which attachments of a version document can
// no longer be restored.
type VersionRestoreSupport interface {
	LoadUnavailableVersionAttachmentKinds(
		context.Context,
		*gorm.DB,
		proto.Message,
	) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
}

// Deps are the shared runtimes the Store drives in the Post transaction.
type Deps struct {
	ContentBlocks *contentblock.Store
	// Signals schedules content.updated in the mutation's transaction.
	Signals          contentsignal.Publisher
	OG               *ogpostgres.Refresher
	ShareLinkCleanup ShareLinkCleanup
	MediaLoader      ContentBlockMediaLoader
	Media            ContentBlockMediaHydrator
	VersionRestore   VersionRestoreSupport
	// CDNDomain builds OG asset URLs; empty serves relative paths.
	CDNDomain string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": d.ContentBlocks == nil, "Signals": d.Signals == nil, "OG": d.OG == nil,
		"ShareLinkCleanup": d.ShareLinkCleanup == nil, "MediaLoader": d.MediaLoader == nil, "Media": d.Media == nil,
		"VersionRestore": d.VersionRestore == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("post store dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Post use cases.
func NewUnits(db *gorm.DB, deps Deps) (mutation.UnitOfWork[post.Store], error) {
	if db == nil {
		return nil, errors.New("post store database is required")
	}
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) post.Store { return Bind(tx, deps) }), nil
}

// Bind returns the Store over tx, for a port another domain drives in its
// own transaction (translation delivery, Series membership, File access).
// Only the methods whose runtime deps names are usable.
func Bind(tx *gorm.DB, deps Deps) post.Store {
	return &store{tx: tx, deps: deps}
}

type store struct {
	tx   *gorm.DB
	deps Deps
}

var _ post.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) post.Store {
	return Bind(tx, s.deps)
}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers a domain sentinel.
type notFoundError struct {
	error
	sentinel error
}

func (err notFoundError) Is(target error) bool { return target == err.sentinel }
func (err notFoundError) Unwrap() error        { return err.error }

// notFound answers gorm's not-found error as sentinel.
func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}

// slugTakenError is the unique violation of a new Post's slug.
type slugTakenError struct{ error }

func (err slugTakenError) Is(target error) bool { return target == post.ErrSlugTaken }
func (err slugTakenError) Unwrap() error        { return err.error }

// ProcessDueScheduledPosts publishes the due Posts in tx, the scheduler
// job's transaction (post.ProcessDueScheduledPosts).
func ProcessDueScheduledPosts(ctx context.Context, tx *gorm.DB, limit int) ([]string, error) {
	return post.ProcessDueScheduledPosts(ctx, Bind(tx, Deps{}), limit)
}
