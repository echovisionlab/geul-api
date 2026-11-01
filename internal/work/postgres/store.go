// Package workpostgres is the PostgreSQL work.Store: the work tables, and
// the shared content, media, OG, route, share link and collaboration
// runtimes a Work transaction drives, bound to one gorm transaction.
package workpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

// ShareLinks removes the share links of a Work in the deleting transaction;
// Share Link owns the rows.
type ShareLinks interface {
	DeleteEntityShareLinks(ctx context.Context, tx *gorm.DB, workID string) error
}

// BlockMedia adds private editor delivery inside the same transaction that
// owns Work lifecycle and principal authorization.
type BlockMedia interface {
	HydrateAuthorizedWorkBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*contentv1.ContentBlockMediaItem,
	) ([]*contentv1.ContentBlockMediaItem, error)
}

// Runtime is the shared OG and File/media lifecycle a Work transaction
// drives; internal/adapters/work implements it.
type Runtime interface {
	RequestCurrentWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string, bool, string) (string, error)
	CancelAndReleaseEntityWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string) error
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	ReadyPublicAssetRefForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error)
	LoadUnavailableVersionAttachmentKinds(context.Context, *gorm.DB, proto.Message) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
}

// Ports are the shared runtimes and other domains the Store drives in the
// Work transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	Signals       contentsignal.Publisher
	// Checkpoints authorize the contributors of a collaboration save.
	Checkpoints persistencecheckpoint.ContributorFence
	Runtime     Runtime
	Media       BlockMedia
	ShareLinks  ShareLinks
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "Signals": p.Signals == nil, "Checkpoints": p.Checkpoints == nil,
		"Runtime": p.Runtime == nil, "Media": p.Media == nil, "ShareLinks": p.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("work store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Work use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[work.Store], error) {
	if db == nil {
		return nil, errors.New("work store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) work.Store { return NewStore(tx, ports) }), nil
}

// Bind joins tx, a transaction or database handle another domain holds, for
// a Work step that runs in it (translation delivery and interchange, a
// policy check, a public read). Only the methods whose ports are set are
// usable. A nil audit stays nil: the joined step is unaudited.
func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) work.Joined {
	return work.Joined{Store: NewStore(tx, ports), Audit: domainaudit.Bind(tx, audit)}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ work.Store = (*store)(nil)

func NewStore(tx *gorm.DB, ports Ports) *store {
	return &store{tx: tx, ports: ports}
}

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) work.Store {
	return NewStore(tx, s.ports)
}

// sentinelError keeps the driver's error readable and in the chain while it
// answers a domain sentinel.
type sentinelError struct {
	error
	sentinel error
}

func (err sentinelError) Is(target error) bool { return target == err.sentinel }
func (err sentinelError) Unwrap() error        { return err.error }

// notFound answers a missing row as sentinel.
func notFound(err error, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sentinelError{error: err, sentinel: sentinel}
	}
	return err
}

// rowChanged answers a write that matched no row.
var rowChanged = sentinelError{error: gorm.ErrRecordNotFound, sentinel: work.ErrRowChanged}

// slugTaken answers a unique violation, the slug another Work holds.
func slugTaken(err error) error {
	if err != nil && dberrors.IsUniqueViolation(err) {
		return sentinelError{error: err, sentinel: work.ErrSlugTaken}
	}
	return err
}

func lockStrength(lock work.RowLock) string {
	switch lock {
	case work.ShareLock:
		return clause.LockingStrengthShare
	case work.UpdateLock:
		return clause.LockingStrengthUpdate
	default:
		return ""
	}
}

func withLock(query *gorm.DB, lock work.RowLock) *gorm.DB {
	if strength := lockStrength(lock); strength != "" {
		return query.Clauses(clause.Locking{Strength: strength})
	}
	return query
}
