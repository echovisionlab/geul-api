// Package mutation runs a domain mutation through the shared primitives in
// one fixed order, inside one database transaction:
//
//  1. lifecycle locks: the global authorization lock when the mutation writes
//     SpiceDB relationships or changes another Identity, then those Identities;
//  2. the domain root locks, when the mutation names them;
//  3. the caller's identity fence and fresh authorization (identitystate);
//  4. the domain change;
//  5. the audit records (domainaudit), which every mutation appends or
//     explicitly declines because nothing changed;
//  6. outbox work: PGMQ commands and content signals in the same transaction;
//  7. SpiceDB relationship writes (authzmutation), compensated on rollback.
//
// It wraps those primitives and does not reimplement them. A domain that has
// moved to Run no longer opens transactions or calls authzmutation.Execute
// itself; internal/architecture enforces that per migrated package.
package mutation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// Runner holds the collaborators every mutation needs.
type Runner struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
	audit   domainaudit.Appender
}

// NewRunner returns a Runner. Every collaborator is required: a mutation
// cannot skip authorization or audit.
func NewRunner(db *gorm.DB, spiceDB *auth.SpiceDBClient, audit domainaudit.Appender) (*Runner, error) {
	if db == nil {
		return nil, errors.New("mutation database is required")
	}
	if spiceDB == nil {
		return nil, errors.New("mutation SpiceDB client is required")
	}
	if audit == nil {
		return nil, errors.New("mutation audit appender is required")
	}
	return &Runner{db: db, spiceDB: spiceDB, audit: audit}, nil
}

// Spec describes one mutation. Access, Change and Audit are required.
type Spec[R any] struct {
	// Access authorizes the caller inside the transaction.
	Access Access
	// Subjects are the Identities whose Member state the mutation changes on
	// behalf of another principal. They are locked after the global
	// authorization lock and before Access, the order every Identity
	// lifecycle mutation takes.
	Subjects []string
	// Lock takes the domain root row locks before Access, so a mutation
	// that waited on a concurrent writer of the same root authorizes the
	// caller as of when it proceeds. It also answers a missing root before
	// authorization, as the domain's lock-first mutations always did.
	Lock func(ctx context.Context, tx *gorm.DB) error
	// Change performs the domain work in tx and returns its result.
	Change func(ctx context.Context, tx *gorm.DB) (R, error)
	// Audit names the record of the result, or Unchanged.
	Audit func(R) Audit
	// Outbox enqueues the PGMQ commands and content signals of the result
	// in tx (contentsignal.TransactionExecutor, PublishInTransaction).
	Outbox func(ctx context.Context, tx *gorm.DB, result R) error
	// Relationships returns the exact SpiceDB relationship batch of the
	// result and its exact inverse. When set, the batch must not be empty.
	Relationships func(R) (apply, compensate []policyv1.RelationshipMutation, err error)
}

// Run executes spec and returns the committed result.
func Run[R any](ctx context.Context, runner *Runner, spec Spec[R]) (R, error) {
	var result R
	if spec.Access.check == nil && spec.Access.err == nil {
		return result, errors.New("mutation access is required")
	}
	if spec.Change == nil {
		return result, errors.New("mutation change is required")
	}
	if spec.Audit == nil {
		return result, errors.New("mutation audit is required")
	}
	subjects, err := lockOrder(spec.Subjects)
	if err != nil {
		return result, err
	}
	if len(subjects) != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, identitystate.MutationTimeout)
		defer cancel()
	}
	work := func(tx *gorm.DB) error {
		for _, subject := range subjects {
			if err := identitystate.Lock(tx, subject); err != nil {
				return fmt.Errorf("lock identity state: %w", err)
			}
		}
		if spec.Lock != nil {
			if err := spec.Lock(ctx, tx); err != nil {
				return err
			}
		}
		if err := spec.Access.authorize(ctx, tx, runner.spiceDB); err != nil {
			return err
		}
		var err error
		if result, err = spec.Change(ctx, tx); err != nil {
			return err
		}
		if err := runner.appendAudit(ctx, tx, spec.Audit(result)); err != nil {
			return err
		}
		if spec.Outbox != nil {
			return spec.Outbox(ctx, tx, result)
		}
		return nil
	}
	if spec.Relationships != nil {
		_, err = authzmutation.Execute(ctx, runner.db, runner.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
			if err := work(tx); err != nil {
				return err
			}
			apply, compensate, err := spec.Relationships(result)
			if err != nil {
				return err
			}
			return write(apply, compensate)
		})
	} else {
		err = runner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if len(subjects) != 0 {
				if err := authzmutation.LockTransaction(tx); err != nil {
					return err
				}
			}
			return work(tx)
		})
	}
	if err != nil {
		var zero R
		return zero, err
	}
	return result, nil
}

// lockOrder sorts and deduplicates subjects so concurrent mutations lock
// Identities in one order.
func lockOrder(subjects []string) ([]string, error) {
	ordered := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		subject = strings.TrimSpace(subject)
		if subject == "" {
			return nil, errors.New("mutation subject identity is required")
		}
		ordered = append(ordered, subject)
	}
	slices.Sort(ordered)
	return slices.Compact(ordered), nil
}

// Access decides inside the transaction whether the caller may mutate.
type Access struct {
	check func(context.Context, *gorm.DB, *auth.SpiceDBClient) error
	err   error
}

func (a Access) authorize(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) error {
	if a.err != nil {
		return a.err
	}
	return a.check(ctx, tx, spiceDB)
}

// FreshCan requires an active, onboarded principal that holds can, read with
// full consistency (identitystate.RequireFreshCan). It takes the generated
// descriptor's result directly: FreshCan(policyv1.Post.Edit(id)).
func FreshCan(can policyv1.Can, err error) Access {
	if err != nil {
		return Access{err: fmt.Errorf("build authorization check: %w", err)}
	}
	return Access{check: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) error {
		return identitystate.RequireFreshCan(ctx, tx, spiceDB, can)
	}}
}

// FreshAdminCan is FreshCan for an administrator-only mutation: a denial
// answers "admin access required" (identitystate.RequireFreshAdminCan).
func FreshAdminCan(can policyv1.Can, err error) Access {
	if err != nil {
		return Access{err: fmt.Errorf("build authorization check: %w", err)}
	}
	return Access{check: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) error {
		return identitystate.RequireFreshAdminCan(ctx, tx, spiceDB, can)
	}}
}

// Require runs FreshCan's check for can in the mutation's transaction.
type Require func(can policyv1.Can) error

// Locked authorizes against the target state Change will see: check locks
// the target row in tx, picks the action its locked state requires (Edit or
// EditArchived), and calls require with it. Change runs after check returns,
// in the same transaction, so the state cannot move between the two.
func Locked(check func(ctx context.Context, tx *gorm.DB, require Require) error) Access {
	if check == nil {
		return Access{err: errors.New("mutation locked access check is required")}
	}
	return Access{check: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) error {
		return check(ctx, tx, func(can policyv1.Can) error {
			return identitystate.RequireFreshCan(ctx, tx, spiceDB, can)
		})
	}}
}

// IsDenied reports whether err is the permission denial of a fresh check,
// for a domain that answers it differently (a masked target answers not
// found).
func IsDenied(err error) bool {
	classified, ok := errs.Classified(err)
	return ok && classified.Code() == connect.CodePermissionDenied
}

// IsUnauthenticated reports whether err rejects a missing or stale session,
// for a domain that masks it like a denial.
func IsUnauthenticated(err error) bool {
	classified, ok := errs.Classified(err)
	return ok && classified.Code() == connect.CodeUnauthenticated
}

// IsNotFound reports whether err answers a missing resource, for a domain
// that reads a shared helper's answer (a retired authorization target).
func IsNotFound(err error) bool {
	classified, ok := errs.Classified(err)
	return ok && classified.Code() == connect.CodeNotFound
}

// IsClassified reports whether err already carries a client answer, a
// Connect or use-case error, for a domain that maps only the store failures
// nothing has answered yet.
func IsClassified(err error) bool {
	_, ok := errs.Classified(err)
	return ok
}

// PrincipalFence locks the authenticated principal's Identity without an
// action check, for a principal's own lifecycle transition that Change
// guards by state (onboarding, before the principal is onboarded).
func PrincipalFence(principal *auth.UserInfo) Access {
	if principal == nil || principal.IdentityID.String() == "" {
		return Access{err: errors.New("mutation principal is required")}
	}
	identityID := principal.IdentityID.String()
	return Access{check: func(_ context.Context, tx *gorm.DB, _ *auth.SpiceDBClient) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return fmt.Errorf("lock identity state: %w", err)
		}
		return nil
	}}
}

// Audit names the record a committed change appends.
type Audit struct {
	action    sharedtelemetry.AuditAction
	build     domainaudit.Builder
	memberID  string
	unchanged bool
	all       []Audit
}

// RequestAudit attributes the record to the request's member actor.
func RequestAudit(action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit {
	return Audit{action: action, build: build}
}

// MemberAudit attributes the record to memberID, for a principal whose
// request has no member actor yet (onboarding).
func MemberAudit(memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) Audit {
	return Audit{action: action, build: build, memberID: memberID}
}

// Audits names every record of a mutation that changes several audited
// facets at once, such as a Series update that edits metadata and moves its
// lifecycle. Unchanged entries are skipped; when every entry is unchanged
// the mutation declines its record.
func Audits(audits ...Audit) Audit {
	changed := make([]Audit, 0, len(audits))
	for _, audit := range audits {
		if !audit.unchanged {
			changed = append(changed, audit)
		}
	}
	if len(changed) == 0 {
		return Unchanged()
	}
	return Audit{all: changed}
}

// Unchanged declines the record because the mutation changed nothing.
func Unchanged() Audit {
	return Audit{unchanged: true}
}

func (r *Runner) appendAudit(ctx context.Context, tx *gorm.DB, audit Audit) error {
	switch {
	case len(audit.all) != 0:
		for _, each := range audit.all {
			if err := r.appendAudit(ctx, tx, each); err != nil {
				return err
			}
		}
		return nil
	case audit.unchanged:
		return nil
	case audit.action == "" || audit.build == nil:
		return errors.New("mutation audit record is required; return Unchanged when nothing changed")
	case audit.memberID != "":
		return domainaudit.AppendMember(ctx, tx, r.audit, audit.memberID, audit.action, audit.build)
	default:
		return domainaudit.AppendRequest(ctx, tx, r.audit, audit.action, audit.build)
	}
}
