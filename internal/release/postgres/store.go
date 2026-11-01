// Package postgres is the PostgreSQL release.Store: the Release and Track
// tables, and the shared content, media, OG, signal and collaboration
// runtimes a Release transaction drives, bound to one gorm transaction.
package releasepostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TrackFiles is the File domain's check that a Track has no audio upload
// session, in the Release transaction.
type TrackFiles interface {
	RequireNoTrackUploadSessionsWithDB(ctx context.Context, tx *gorm.DB, trackID string) error
}

// Assets is the shared File and public asset lifecycle of Release artwork.
type Assets interface {
	LockAttachableFiles(ctx context.Context, tx *gorm.DB, fileIDs []string) error
	BindArtwork(ctx context.Context, tx *gorm.DB, fileID, releaseID string) (*commonv1.AssetRef, error)
	ReleaseArtwork(ctx context.Context, tx *gorm.DB, releaseID string) error
	ReadyArtwork(ctx context.Context, db *gorm.DB, fileID string) (*commonv1.AssetRef, error)
	ReadyAssets(ctx context.Context, db *gorm.DB, candidateIDs ...*string) (map[string]*commonv1.AssetRef, error)
}

// OG cancels Release-generated Open Graph work and releases its image.
type OG interface {
	CancelAndRelease(ctx context.Context, tx *gorm.DB, releaseID string) error
}

// ContentOG requests current Release Open Graph work after content
// mutations.
type ContentOG interface {
	RequestCurrent(ctx context.Context, tx *gorm.DB, releaseID, reason string) error
}

// ArtistSummaries and LabelSummaries read the summaries of the Artists and
// Labels a Release references; the Release adapter implements them.
type ArtistSummaries interface {
	LoadArtistSummariesWithDB(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]release.ArtistSummary, error)
}

type LabelSummaries interface {
	LoadLabelSummariesWithDB(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]release.LabelSummary, error)
}

// ShareLinks removes a deleted Release's share links in its transaction; the
// share link adapter implements it.
type ShareLinks interface {
	DeleteEntityShareLinks(ctx context.Context, tx *gorm.DB, entityID string) error
}

// Ports are the other domains' adapters the Store drives in the Release
// transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	Publisher     contentsignal.Publisher
	Checkpoints   persistencecheckpoint.ContributorFence
	TrackFiles    TrackFiles
	Assets        Assets
	OG            OG
	ContentOG     ContentOG
	Artists       ArtistSummaries
	Labels        LabelSummaries
	ShareLinks    ShareLinks
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "Publisher": p.Publisher == nil, "Checkpoints": p.Checkpoints == nil,
		"TrackFiles": p.TrackFiles == nil, "Assets": p.Assets == nil, "OG": p.OG == nil, "ContentOG": p.ContentOG == nil,
		"Artists": p.Artists == nil, "Labels": p.Labels == nil, "ShareLinks": p.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("release store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Release use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[release.Store], error) {
	if db == nil {
		return nil, errors.New("release store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) release.Store { return NewStore(tx, ports) }), nil
}

// NewStore returns the Store over tx. Only the methods whose ports are set
// are usable.
func NewStore(tx *gorm.DB, ports Ports) release.Store {
	return &store{tx: tx, ports: ports}
}

// Bind joins the transaction another domain opened (translation delivery
// and interchange, a source-locale switch, a File ingest): the Store over
// tx, and audit appending in tx. A nil audit leaves Joined.Audit nil for a
// step that records nothing.
func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) release.Joined {
	joined := release.Joined{Store: NewStore(tx, ports)}
	if audit != nil {
		joined.Audit = boundAudit{audit: audit, tx: tx}
	}
	return joined
}

// boundAudit appends in the joined transaction whatever transaction the
// Release step names, which is nil.
type boundAudit struct {
	audit domainaudit.Appender
	tx    *gorm.DB
}

func (b boundAudit) AppendDomainAuditInTransaction(ctx context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	return b.audit.AppendDomainAuditInTransaction(ctx, b.tx, record)
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ release.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back to
// a domain callback.
func (s *store) with(tx *gorm.DB) release.Store {
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

// notFoundAs answers gorm's not-found error as sentinel.
func notFoundAs(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sentinelError{error: err, sentinel: sentinel}
	}
	return err
}

// slugTaken answers a unique violation as release.ErrSlugTaken: the slug is
// the only unique column a Release write sets.
func slugTaken(err error) error {
	if dberrors.IsUniqueViolation(err) {
		return sentinelError{error: err, sentinel: release.ErrSlugTaken}
	}
	return err
}

// locking is the row lock clause of lock; Unlocked has none.
func locking(lock release.RowLock) (clause.Locking, bool) {
	switch lock {
	case release.KeyShareLock:
		return clause.Locking{Strength: "KEY SHARE"}, true
	case release.ShareLock:
		return clause.Locking{Strength: clause.LockingStrengthShare}, true
	case release.UpdateLock:
		return clause.Locking{Strength: clause.LockingStrengthUpdate}, true
	default:
		return clause.Locking{}, false
	}
}

// withLock adds lock's clause to query.
func withLock(query *gorm.DB, lock release.RowLock) *gorm.DB {
	if locked, ok := locking(lock); ok {
		return query.Clauses(locked)
	}
	return query
}
