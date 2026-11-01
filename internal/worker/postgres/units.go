// Package workerpostgres binds the worker's ports to PostgreSQL, object
// storage and the runtimes of the domains the worker drives. The worker owns
// no table: its unit of work opens one transaction per job and binds each
// domain's port to it, and its other ports call the owning domains'
// runtimes, which open their own transactions.
package workerpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/post"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/worker"
)

// MetadataAIRecoverer republishes expired metadata AI jobs in its own unit
// of work (*ai.MetadataJobManager).
type MetadataAIRecoverer interface {
	RecoverExpiredJobs(ctx context.Context, limit int) (int, error)
}

// Ports are the runtimes a job transaction calls.
type Ports struct {
	Audit      domainaudit.Appender // the legal activation's audit records
	OGPlanner  *ogpostgres.Planner
	MetadataAI MetadataAIRecoverer // optional: the recovery is then never bound
}

func (ports Ports) validate() error {
	var missing []error
	if ports.Audit == nil {
		missing = append(missing, errors.New("worker job audit appender is required"))
	}
	if ports.OGPlanner == nil {
		missing = append(missing, errors.New("worker job OG planner is required"))
	}
	return errors.Join(missing...)
}

// NewUnits returns the worker's unit of work: one read-committed transaction
// per job, with each domain's port bound to it.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[worker.Jobs], error) {
	if db == nil {
		return nil, errors.New("worker job database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) worker.Jobs { return Bind(tx, ports) }), nil
}

// Bind returns the job ports bound to tx.
func Bind(tx *gorm.DB, ports Ports) worker.Jobs {
	return jobs{tx: tx, ports: ports}
}

type jobs struct {
	tx    *gorm.DB
	ports Ports
}

func (j jobs) ScheduledPosts() post.Store {
	return postpostgres.Bind(j.tx, postpostgres.Deps{})
}

func (j jobs) MetadataAIRecovery() worker.MetadataAIRecovery {
	return metadataAIRecovery(j)
}

func (j jobs) LegalActivation() worker.LegalActivation {
	return legalActivation(j)
}

type metadataAIRecovery jobs

func (r metadataAIRecovery) RecoverExpiredJobs(ctx context.Context, limit int) (int, error) {
	if r.ports.MetadataAI == nil {
		return 0, errors.New("metadata AI job recovery is not configured")
	}
	return r.ports.MetadataAI.RecoverExpiredJobs(ctx, limit)
}

type legalActivation jobs

func (l legalActivation) ActivateDue(
	ctx context.Context,
	kind worker.LegalKind,
	documentID, eventKey string,
	templateData map[string]string,
	now time.Time,
) (worker.EffectiveNotice, bool, error) {
	joined := legalpostgres.Bind(l.tx, legalpostgres.Ports{
		Routes: legaladapter.NewRuntime(), Notices: legaladapter.NewNoticeRuntime(),
	}, l.ports.Audit)
	activated, run, err := legaldomain.ActivateDuePolicy(
		ctx,
		joined,
		legalPolicyKind(kind),
		documentID,
		eventKey,
		templateData,
		now,
	)
	if err != nil || !activated {
		return nil, false, err
	}
	return run, true, nil
}

func (l legalActivation) RequestOG(ctx context.Context, kind worker.LegalKind, documentID, reason string) error {
	_, err := legaladapter.RequestSaved(ctx, l.tx, l.ports.OGPlanner, string(kind), documentID, "", true, reason)
	return err
}
