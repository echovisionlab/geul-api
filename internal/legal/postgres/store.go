// Package legalpostgres is the PostgreSQL legal.Store: the Terms and Privacy
// history and translation tables, and the shared content, OG, notice, Share
// Link, session and Member runtimes a policy transaction drives, bound to
// one gorm transaction.
package legalpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Routes is the public route of a policy kind and its OG generation;
// adapters/legal.Runtime implements it.
type Routes interface {
	LockActivation(ctx context.Context, tx *gorm.DB, kind string) error
	CurrentForRoute(ctx context.Context, db *gorm.DB, kind string) (*legal.CurrentRoute, error)
	RequestSaved(ctx context.Context, tx *gorm.DB, kind, documentID, requestedLocale string, allLocales bool, reason string) error
	RouteID(kind string) string
	CancelAndRelease(ctx context.Context, tx *gorm.DB, kind, ownerID string) error
}

// Notices seals and stops the Campaign delivery runs of legal notices in
// the policy transaction; adapters/legal.NoticeRuntime implements it. kind
// is "terms" or "privacy".
type Notices interface {
	CreateRun(ctx context.Context, tx *gorm.DB, kind, policyID, eventKey string, templateData map[string]string, scheduledAt time.Time) (*model.CampaignDeliveryRun, error)
	LatestSealedVersion(ctx context.Context, tx *gorm.DB, kind string) (int, error)
	CancelScheduledRuns(ctx context.Context, tx *gorm.DB, kind, policyID string) error
	StopRunsForDeletedPolicy(ctx context.Context, tx *gorm.DB, kind, policyID string) error
	UpdateNoticeRuns(ctx context.Context, tx *gorm.DB, kind, policyID string) ([]model.CampaignDeliveryRun, error)
}

// PreviewLinks writes the Share Links of versions, which the Share Link
// domain owns; sharelinkadapter.LegalPreviewLinks implements it.
type PreviewLinks interface {
	CreateAutomaticPreview(ctx context.Context, tx *gorm.DB, preview legal.AutomaticPreview) error
	DeleteAutomaticPreview(ctx context.Context, tx *gorm.DB, token, entityType, entityID string) error
	DeleteForEntity(ctx context.Context, tx *gorm.DB, entityType, entityID string) error
}

// Members reads the locale a Member prefers, which seeds the source locale
// of a version they create; memberadapter.PreferredLocales implements it.
type Members interface {
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
}

// Ports are the other domains and shared runtimes the Store drives in the
// policy transaction.
type Ports struct {
	ContentBlocks *contentblock.Store
	Routes        Routes
	Notices       Notices
	PreviewLinks  PreviewLinks
	Members       Members
	// Checkpoints re-resolves the contributors a collaboration write is
	// attributed to.
	Checkpoints persistencecheckpoint.ContributorFence
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "Routes": p.Routes == nil, "Notices": p.Notices == nil,
		"PreviewLinks": p.PreviewLinks == nil, "Members": p.Members == nil, "Checkpoints": p.Checkpoints == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("legal store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Terms and Privacy use
// cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[legal.Store], error) {
	if db == nil {
		return nil, errors.New("legal store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) legal.Store { return newStore(tx, ports) }), nil
}

// Bind joins tx, a transaction another pipeline opened: a scheduled
// activation, a notice dispatch, a translation apply or an interchange.
// Only the methods whose ports are set are usable; a nil audit leaves the
// step unaudited.
func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) legal.Joined {
	joined := legal.Joined{Store: newStore(tx, ports)}
	if audit != nil {
		joined.Audit = boundAudit{audit: audit, tx: tx}
	}
	return joined
}

// boundAudit appends in the joined transaction whatever transaction the
// Legal step names, which is nil.
type boundAudit struct {
	audit domainaudit.Appender
	tx    *gorm.DB
}

func (b boundAudit) AppendDomainAuditInTransaction(ctx context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	return b.audit.AppendDomainAuditInTransaction(ctx, b.tx, record)
}

func newStore(tx *gorm.DB, ports Ports) *store {
	return &store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ legal.Store = (*store)(nil)

// with binds the Store to tx, the transaction a shared runtime hands back
// to a domain callback.
func (s *store) with(tx *gorm.DB) legal.Store {
	return newStore(tx, s.ports)
}

func historyTable(kind legal.PolicyKind) string     { return string(kind) + "_history" }
func translationTable(kind legal.PolicyKind) string { return string(kind) + "_translation" }

// status is the stored status of kind in lifecycle state.
func status(kind legal.PolicyKind, state string) legal.Status {
	stored, _ := legal.PolicyStatus(kind, state)
	return stored
}

// locking is the row lock clause of lock, nil without one.
func locking(lock legal.RowLock) []clause.Expression {
	switch lock {
	case legal.ShareLock:
		return []clause.Expression{clause.Locking{Strength: clause.LockingStrengthShare}}
	case legal.UpdateLock:
		return []clause.Expression{clause.Locking{Strength: clause.LockingStrengthUpdate}}
	default:
		return nil
	}
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
