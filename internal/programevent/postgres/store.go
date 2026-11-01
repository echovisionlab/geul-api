// Package programeventpostgres is the PostgreSQL programevent.Store: the
// program_event tables, and the shared content, media, route, session and
// Member runtimes a Program Event transaction drives, bound to one gorm
// transaction.
package programeventpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/programevent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

// Media is the shared File and public asset lifecycle of event media and
// series posters; programeventadapter.Runtime implements it.
type Media interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error)
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ReadyPublicAssetRefForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	ResolveSingleReadyInlineAssetForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
}

// Files adds request-scoped delivery to already-authorized Block media.
type Files interface {
	HydrateAuthorizedProgramEventBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*contentv1.ContentBlockMediaItem,
	) ([]*contentv1.ContentBlockMediaItem, error)
}

// Members is the Member-owned surface Program Event reads.
type Members interface {
	// PreferredLocale returns the stored preferred locale of the live
	// Member, or nil.
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
	// LockMembers takes a key-share lock on the Members memberIDs name and
	// returns how many exist.
	LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) (int, error)
}

// CreditArtists reads the Artist-owned projection of a credited artist.
type CreditArtists interface {
	// CreditArtist returns the artist's ID, source-locale name and slug; an
	// empty ID means the artist does not exist.
	CreditArtist(ctx context.Context, db *gorm.DB, artistID string) (id, name string, slug *string, err error)
	// FirstImageFileID returns the File of the artist's first image, or
	// empty.
	FirstImageFileID(ctx context.Context, db *gorm.DB, artistID string) (string, error)
}

// Ports are the other domains and shared runtimes the Store drives in the
// Program Event transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	// Signals schedules content.updated in the transaction.
	Signals       contentsignal.Publisher
	Media         Media
	Files         Files
	Members       Members
	CreditArtists CreditArtists
	// Checkpoints re-resolves the contributors a collaboration write is
	// attributed to.
	Checkpoints persistencecheckpoint.ContributorFence
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "Signals": p.Signals == nil, "Media": p.Media == nil,
		"Files": p.Files == nil, "Members": p.Members == nil, "CreditArtists": p.CreditArtists == nil,
		"Checkpoints": p.Checkpoints == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("program event store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Program Event use
// cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[programevent.Store], error) {
	if db == nil {
		return nil, errors.New("program event store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) programevent.Store { return NewStore(tx, ports) }), nil
}

// Bind joins tx, a transaction another pipeline opened: translation
// delivery and interchange, or an attachment check. Only the methods whose
// ports are set are usable; a nil audit leaves the step unaudited.
func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) programevent.Joined {
	return programevent.Joined{Store: NewStore(tx, ports), Audit: domainaudit.Bind(tx, audit)}
}

func NewStore(tx *gorm.DB, ports Ports) *store {
	return &store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ programevent.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) programevent.Store {
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

// notFound answers gorm's not-found error as sentinel.
func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}

// violationError keeps a constraint violation readable and in the chain
// while it answers a domain sentinel.
type violationError struct {
	error
	sentinel error
}

func (err violationError) Is(target error) bool { return target == err.sentinel }
func (err violationError) Unwrap() error        { return err.error }

// slugTaken answers a unique violation as programevent.ErrSlugTaken.
func slugTaken(err error) error {
	if dberrors.IsUniqueViolation(err) {
		return violationError{error: err, sentinel: programevent.ErrSlugTaken}
	}
	return err
}

// typeInUse answers a foreign key violation as programevent.ErrTypeInUse.
func typeInUse(err error) error {
	if dberrors.IsForeignKeyViolation(err) {
		return violationError{error: err, sentinel: programevent.ErrTypeInUse}
	}
	return err
}
