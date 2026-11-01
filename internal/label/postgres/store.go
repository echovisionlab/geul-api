// Package labelpostgres is the PostgreSQL label.Store: the label tables, and the
// shared content, media, OG, route and relation runtimes a Label transaction
// drives, bound to one gorm transaction.
package labelpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	entityparticipantpostgres "github.com/echovisionlab/geul-api/internal/entityparticipant/postgres"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Translation provides Label's initial source locale and the shared
// translation contributor check.
type Translation interface {
	ResolveInitialSourceLocale(context.Context, *gorm.DB, string) string
	RequireDocumentContributors(context.Context, *gorm.DB, []string) error
}

// Runtime is Label's narrow boundary to shared File/media and OG lifecycles.
// Label owns mutation policy and binding keys; adapters own CDN projection and
// the concrete shared runtime.
type Runtime interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error)
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ReleaseExactPublicAssetBindings(context.Context, *gorm.DB, string, string, []string) error
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	ResolveReadySourceFileRefs(context.Context, *gorm.DB, []string, ...string) (map[string]*commonv1.AssetRef, error)
	RequestCurrentWithDB(context.Context, *gorm.DB, string, string) (string, error)
	CancelAndReleaseOGWithDB(context.Context, *gorm.DB, string) error
}

// ArtistRelations reads the Artists linked to Labels; the Artist adapter
// implements it. Lists are ordered by ID text.
type ArtistRelations interface {
	ArtistsOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error)
	CountArtistsByLabel(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]int32, error)
}

// ReleaseRelations reads the Releases linked to Labels; the Release adapter
// implements it. Lists are ordered by ID text.
type ReleaseRelations interface {
	ReleasesOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error)
	CountReleasesByLabel(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]int32, error)
}

// ProgramEventRelations reads the Program Events linked to a Label; the
// Program Event adapter implements it. The list is ordered by ID text.
type ProgramEventRelations interface {
	ProgramEventsOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error)
}

// ShareLinks removes a deleted Label's share links in its transaction; the
// share link adapter implements it.
type ShareLinks interface {
	DeleteLabelShareLinks(ctx context.Context, tx *gorm.DB, labelID string) error
}

// Ports are the shared runtimes the Store drives in the Label transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	Publisher     contentsignal.Publisher
	Checkpoints   persistencecheckpoint.ContributorFence
	Translation   Translation
	Runtime       Runtime
	Artists       ArtistRelations
	Releases      ReleaseRelations
	ProgramEvents ProgramEventRelations
	ShareLinks    ShareLinks
}

func (d Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": d.ContentBlocks == nil, "Publisher": d.Publisher == nil, "Checkpoints": d.Checkpoints == nil,
		"Translation": d.Translation == nil, "Runtime": d.Runtime == nil, "Artists": d.Artists == nil,
		"Releases": d.Releases == nil, "ProgramEvents": d.ProgramEvents == nil, "ShareLinks": d.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("label store dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Label use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[label.Store], error) {
	if db == nil {
		return nil, errors.New("label store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) label.Store { return NewStore(tx, ports) }), nil
}

// NewStore returns the Store over tx, for a port another domain drives in its
// own transaction (translation delivery). Only methods whose required ports
// are set are usable.
func NewStore(tx *gorm.DB, ports Ports) label.Store {
	return &store{Store: entityparticipantpostgres.New(tx, labelTables), tx: tx, ports: ports}
}

var labelTables = entityparticipantpostgres.Tables{
	Table: "label", IDColumn: "label_id", OwnerTable: "label_owner", ManagerTable: "label_manager",
	ParentColumn: "parent_label_id", ParentGraphLock: "label_parent_graph",
}

type store struct {
	entityparticipantpostgres.Store
	tx    *gorm.DB
	ports Ports
}

var _ label.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) label.Store {
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

// labelNotFound answers gorm's not-found error as label.ErrLabelNotFound.
func labelNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: label.ErrLabelNotFound}
	}
	return err
}

// localeNotFound answers gorm's not-found error as label.ErrLocaleNotFound.
func localeNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: label.ErrLocaleNotFound}
	}
	return err
}
