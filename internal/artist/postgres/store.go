// Package artistpostgres is the PostgreSQL artist.Store: the artist tables, and
// the shared content, media, OG, route and relation runtimes an Artist
// transaction drives, bound to one gorm transaction.
package artistpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	entityparticipantpostgres "github.com/echovisionlab/geul-api/internal/entityparticipant/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Translation is the Artist-owned port to the shared translation runtime.
type Translation interface {
	ResolveInitialSourceLocale(context.Context, *gorm.DB, string) string
	RequireDocumentContributors(context.Context, *gorm.DB, []string) error
}

// Runtime is the Artist-owned boundary for shared media, OG, and route
// infrastructure. Implementations live under internal/adapters/artist.
type Runtime interface {
	LoadArtistImages(context.Context, *gorm.DB, []string) (map[string][]artist.ArtistImageProjection, error)
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	ReplaceArtistImageBindings(context.Context, *gorm.DB, string, []string) error
	RequestCurrentWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string, bool, string) (string, error)
	CancelAndReleaseEntityWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string) error
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	EnsureResourceRouteAvailable(context.Context, *gorm.DB, string, string, string) error
	EnsureResourceRouteAvailableInTx(context.Context, *gorm.DB, string, string, string) error
	IsResourceRouteAvailable(context.Context, *gorm.DB, string, string) (bool, error)
}

// Labels reads the Labels an Artist links; the Label adapter implements it.
type Labels interface {
	// LabelTitles maps each existing Label of labelIDs to its source title.
	LabelTitles(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]string, error)
	// DraftLabelIDs reports which of labelIDs are draft Labels.
	DraftLabelIDs(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]bool, error)
}

// ReleaseRelations reads the Releases and Tracks that credit or link an
// Artist; the Release adapter implements it.
type ReleaseRelations interface {
	ReleasesOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
	TracksOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
	CountReleasesByArtist(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]int32, error)
}

// ProgramEventRelations reads the Program Events that list or credit an
// Artist; the Program Event adapter implements it.
type ProgramEventRelations interface {
	ProgramEventsOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
}

// WorkRelations reads the Works that credit an Artist; the Work adapter
// implements it.
type WorkRelations interface {
	WorksOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
}

// ShareLinks removes a deleted Artist's share links in its transaction; the
// share link adapter implements it.
type ShareLinks interface {
	DeleteArtistShareLinks(ctx context.Context, tx *gorm.DB, artistID string) error
}

// Ports are the shared runtimes the Store drives in the Artist transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	Publisher     contentsignal.Publisher
	Checkpoints   persistencecheckpoint.ContributorFence
	Translation   Translation
	Runtime       Runtime
	Labels        Labels
	Releases      ReleaseRelations
	ProgramEvents ProgramEventRelations
	Works         WorkRelations
	ShareLinks    ShareLinks
}

func (d Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": d.ContentBlocks == nil, "Publisher": d.Publisher == nil, "Checkpoints": d.Checkpoints == nil,
		"Translation": d.Translation == nil, "Runtime": d.Runtime == nil, "Labels": d.Labels == nil,
		"Releases": d.Releases == nil, "ProgramEvents": d.ProgramEvents == nil, "Works": d.Works == nil,
		"ShareLinks": d.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("artist store dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Artist use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[artist.Store], error) {
	if db == nil {
		return nil, errors.New("artist store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) artist.Store { return NewStore(tx, ports) }), nil
}

// NewStore returns the Store over tx, for a port another domain drives in its
// own transaction (translation delivery, the public reads). Only the
// methods whose required ports are set are usable.
func NewStore(tx *gorm.DB, ports Ports) artist.Store {
	return &store{Store: entityparticipantpostgres.New(tx, artistTables), tx: tx, ports: ports}
}

var artistTables = entityparticipantpostgres.Tables{
	Table: "artist", IDColumn: "artist_id", OwnerTable: "artist_owner", ManagerTable: "artist_manager",
	ParentColumn: "parent_artist_id", ParentGraphLock: "artist_parent_graph",
}

type store struct {
	entityparticipantpostgres.Store
	tx    *gorm.DB
	ports Ports
}

var _ artist.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) artist.Store {
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

func notFound(err error, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}
