// Package formpostgres is the PostgreSQL form.Store: the form tables, and
// the shared content, media, OG, route, collaboration, Member and Share Link
// runtimes a Form transaction drives, bound to one gorm transaction.
package formpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/form"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

// Assets owns Form integration with File attachment and public asset
// bindings.
type Assets interface {
	LockAttachableFiles(ctx context.Context, tx *gorm.DB, fileIDs []string) error
	BindFeaturedImage(ctx context.Context, tx *gorm.DB, fileID, formID string) (*mediaasset.Reference, error)
	ReleaseFeaturedImage(ctx context.Context, tx *gorm.DB, formID string) error
	// FeaturedImage resolves the Form's ready featured image; a failure is
	// logged and answers nil.
	FeaturedImage(ctx context.Context, db *gorm.DB, formID string) *mediaasset.Reference
}

// OG owns Form integration with generated Open Graph assets.
type OG interface {
	// Request starts a generation and returns its run ID, empty when none
	// started.
	Request(ctx context.Context, tx *gorm.DB, formID, locale string, allLocales bool, reason string) (string, error)
	CancelAndRelease(ctx context.Context, tx *gorm.DB, formID string) error
	ReadyAsset(ctx context.Context, db *gorm.DB, localizedID, sourceID *string) (*mediaasset.Reference, error)
}

// Routes owns collision checks between Form slugs and the routes of other
// resources.
type Routes interface {
	SlugAvailable(ctx context.Context, db *gorm.DB, slug string) (bool, error)
	EnsureAvailableLocked(ctx context.Context, tx *gorm.DB, slug string) error
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

// Ports are the other domains and shared runtimes the Store drives in the
// Form transaction.
type Ports struct {
	// ContentBlocks advances the Form Content Document revision.
	ContentBlocks *contentblock.Store
	Signals       contentsignal.Publisher
	Assets        Assets
	OG            OG
	Routes        Routes
	Collaboration Collaboration
	Members       Members
	ShareLinks    ShareLinks
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "Signals": p.Signals == nil, "Assets": p.Assets == nil, "OG": p.OG == nil,
		"Routes": p.Routes == nil, "Collaboration": p.Collaboration == nil, "Members": p.Members == nil,
		"ShareLinks": p.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("form store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Form use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[form.Store], error) {
	if db == nil {
		return nil, errors.New("form store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) form.Store { return NewStore(tx, ports) }), nil
}

// NewStore binds the Store to tx. Only the methods whose ports ports names
// are usable, so a step another domain drives in its own transaction binds
// what it needs.
func NewStore(tx *gorm.DB, ports Ports) form.Store {
	return &store{tx: tx, ports: ports}
}

// Bind joins the Store and audit to tx, a transaction another domain's
// pipeline opened (translation delivery and interchange).
func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) form.Joined {
	return form.Joined{Store: NewStore(tx, ports), Audit: domainaudit.Bind(tx, audit)}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ form.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) form.Store {
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

func (s *store) RequireCurrentContributor(ctx context.Context, formID, memberID string) error {
	return s.ports.Collaboration.RequireCurrentContributor(ctx, s.tx, formID, memberID)
}

func (s *store) PreferredLocale(ctx context.Context, memberID string) (*string, error) {
	return s.ports.Members.PreferredLocale(ctx, s.tx, memberID)
}

func (s *store) DeleteShareLinks(ctx context.Context, formID string) error {
	return s.ports.ShareLinks.DeleteFormShareLinks(ctx, s.tx, formID)
}

func (s *store) RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	return translation.LoadRuntimeSettings(ctx, s.tx)
}
