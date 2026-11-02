// Package referencecatalogpostgres is the PostgreSQL Store of the Reference
// Catalog use cases: the category, tag, genre, style, format, client and
// map_place rows, the relations of other domains that use them, and the
// transaction-bound media, Member, Menu and Work adapters.
package referencecatalogpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Assets provides the file-owned asset lifecycle in the catalog's
// transaction (referencecatalogadapter.Assets).
type Assets interface {
	LockForAttachment(context.Context, *gorm.DB, []string) error
	BindReady(context.Context, *gorm.DB, referencecatalog.AssetBinding) (*mediaasset.Reference, error)
	Release(context.Context, *gorm.DB, referencecatalog.AssetRelease) error
	ReadyRef(context.Context, *gorm.DB, referencecatalog.AssetSource) (*mediaasset.Reference, error)
}

// Members resolves the Member summaries a Map Place shows
// (referencecatalogadapter.MemberSummaries).
type Members interface {
	Resolve(context.Context, *gorm.DB, []string) (map[string]*memberref.Summary, error)
}

// MenuTargets keeps menu items consistent when a catalog target changes
// (referencecatalogmenuadapter.Targets).
type MenuTargets interface {
	UpdateSlug(context.Context, *gorm.DB, referencecatalog.MenuTargetSlugChange) error
	Remove(context.Context, *gorm.DB, referencecatalog.MenuTarget) error
}

// ClientWorks counts the Work relations of Clients (workadapter.ClientWorks).
type ClientWorks interface {
	// CountByClient returns how many Works reference each Client; a Client
	// without Works is absent.
	CountByClient(ctx context.Context, db *gorm.DB, clientIDs []string) (map[string]int32, error)
}

// Ports are the other domains' adapters a catalog transaction calls with the
// transaction itself; all are required.
type Ports struct {
	Assets      Assets
	Members     Members
	MenuTargets MenuTargets
	ClientWorks ClientWorks
}

// NewUnits returns the production unit of work of the Reference Catalog use
// cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[referencecatalog.Store], error) {
	var missing []string
	for name, absent := range map[string]bool{
		"database": db == nil, "Assets": ports.Assets == nil, "Members": ports.Members == nil,
		"MenuTargets": ports.MenuTargets == nil, "ClientWorks": ports.ClientWorks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return nil, errors.New("reference catalog store dependencies are required: " + strings.Join(missing, ", "))
	}
	return mutation.Postgres(db, func(tx *gorm.DB) referencecatalog.Store { return store{tx: tx, ports: ports} }), nil
}

// store is the Reference Catalog Store bound to one transaction.
type store struct {
	tx    *gorm.DB
	ports Ports
}

// sentinelError answers a use-case sentinel and keeps the driver or gorm
// error that caused it readable and in the chain.
type sentinelError struct {
	error
	sentinel error
}

func (err sentinelError) Is(target error) bool { return target == err.sentinel }
func (err sentinelError) Unwrap() error        { return err.error }

func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sentinelError{error: err, sentinel: sentinel}
	}
	return err
}

// referenceTaken classifies a unique violation of table by the constraint
// that refused it.
func referenceTaken(table string, err error) error {
	if !dberrors.IsUniqueViolation(err) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case table + "_name_key":
			return sentinelError{error: err, sentinel: referencecatalog.ErrNameTaken}
		case table + "_slug_key":
			return sentinelError{error: err, sentinel: referencecatalog.ErrSlugTaken}
		}
	}
	return sentinelError{error: err, sentinel: referencecatalog.ErrReferenceTaken}
}

func clientNameTaken(err error) error {
	if dberrors.IsUniqueViolation(err) {
		return sentinelError{error: err, sentinel: referencecatalog.ErrClientNameTaken}
	}
	return err
}

func (s store) LockFilesForAttachment(ctx context.Context, fileIDs []string) error {
	return s.ports.Assets.LockForAttachment(ctx, s.tx, fileIDs)
}

func (s store) BindReadyAsset(ctx context.Context, binding referencecatalog.AssetBinding) (*mediaasset.Reference, error) {
	return s.ports.Assets.BindReady(ctx, s.tx, binding)
}

func (s store) ReleaseAssets(ctx context.Context, release referencecatalog.AssetRelease) error {
	return s.ports.Assets.Release(ctx, s.tx, release)
}

func (s store) ReadyAsset(ctx context.Context, source referencecatalog.AssetSource) (*mediaasset.Reference, error) {
	return s.ports.Assets.ReadyRef(ctx, s.tx, source)
}

func (s store) UpdateMenuTargetSlug(ctx context.Context, change referencecatalog.MenuTargetSlugChange) error {
	return s.ports.MenuTargets.UpdateSlug(ctx, s.tx, change)
}

func (s store) RemoveMenuTarget(ctx context.Context, target referencecatalog.MenuTarget) error {
	return s.ports.MenuTargets.Remove(ctx, s.tx, target)
}

func (s store) CountClientWorks(ctx context.Context, clientIDs []string) (map[string]int32, error) {
	return s.ports.ClientWorks.CountByClient(ctx, s.tx, clientIDs)
}

func (s store) MemberSummaries(ctx context.Context, memberIDs []string) (map[string]referencecatalog.MemberSummary, error) {
	resolved, err := s.ports.Members.Resolve(ctx, s.tx, memberIDs)
	if err != nil {
		return nil, err
	}
	members := make(map[string]referencecatalog.MemberSummary, len(resolved))
	for id, summary := range resolved {
		if summary != nil {
			members[id] = referencecatalog.MemberSummary{
				ID: summary.ID, Nickname: summary.Nickname, Avatar: summary.Avatar, Deleted: summary.Deleted,
			}
		}
	}
	return members, nil
}
