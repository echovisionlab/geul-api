package entityparticipant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// PermissionChecker answers SpiceDB authorization decisions.
type PermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

// CheckPermissionForPrincipal reports whether principal may perform action on
// the entity. An anonymous or identity-less principal is simply not allowed.
func CheckPermissionForPrincipal(ctx context.Context, checker PermissionChecker, entityID string, action auth.ResourceAction, principal *auth.UserInfo) (bool, error) {
	if principal == nil || !principal.Authenticated || strings.TrimSpace(principal.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := action(entityID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

// RequireGlobalAction requires the request principal to hold a platform-wide
// action such as create or list.
func RequireGlobalAction(ctx context.Context, checker PermissionChecker, can policyv1.Can) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.NotAuthenticated()
	}
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	if !allowed {
		return errs.AdminRequired()
	}
	return nil
}

// RequireCreate requires the platform-wide create action of the entity.
func (s Spec) RequireCreate(ctx context.Context, checker PermissionChecker) error {
	can, err := s.Policy.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return RequireGlobalAction(ctx, checker, can)
}

// RequireList requires the platform-wide list action of the entity.
func (s Spec) RequireList(ctx context.Context, checker PermissionChecker) error {
	can, err := s.Policy.List()
	if err != nil {
		return errs.Internal(err)
	}
	return RequireGlobalAction(ctx, checker, can)
}

// RequirePermission requires the request principal to hold action on the
// entity. A denial answers not found so the entity's existence stays hidden.
func (s Spec) RequirePermission(ctx context.Context, checker PermissionChecker, entityID string, action auth.ResourceAction) error {
	if auth.GetUser(ctx) == nil {
		return errs.AuthenticationRequired()
	}
	return s.RequirePrincipalPermission(ctx, checker, entityID, action, auth.GetUser(ctx))
}

// RequirePrincipalPermission is RequirePermission for an explicit principal,
// which may be nil.
func (s Spec) RequirePrincipalPermission(ctx context.Context, checker PermissionChecker, entityID string, action auth.ResourceAction, principal *auth.UserInfo) error {
	allowed, err := CheckPermissionForPrincipal(ctx, checker, entityID, action, principal)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), failurecause.WithAttrs(err, slog.String(s.IDColumn, entityID)))
	}
	if !allowed {
		return errs.NotFound(s.Name, entityID)
	}
	return nil
}

// RequireLockedPermission locks the request principal's identity state and
// then requires action on the entity, so a concurrent ban or deletion of the
// principal cannot race the mutation.
func (s Spec) RequireLockedPermission(ctx context.Context, tx *gorm.DB, checker PermissionChecker, entityID string, action auth.ResourceAction) error {
	principal, err := s.LockActivePrincipal(ctx, tx)
	if err != nil {
		return err
	}
	return s.RequirePrincipalPermission(ctx, checker, entityID, action, principal)
}

// LockRoot locks the entity row for update.
func (s Spec) LockRoot(ctx context.Context, tx *gorm.DB, entityID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table(s.Table).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").Where("id = ?::uuid", entityID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound(s.Name, entityID)
		}
		return errs.Internal(err)
	}
	return nil
}

// LockActivePrincipal locks the request principal's identity state. It
// returns nil without an error when the principal is no longer active.
func (s Spec) LockActivePrincipal(ctx context.Context, tx *gorm.DB) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("lock %s mutation principal: %w", s.Title, err))
	}
	if !active {
		return nil, nil
	}
	return principal, nil
}

// ActionCheck pairs a SpiceDB action with the domain action it grants.
type ActionCheck[A any] struct {
	Can    auth.ResourceAction
	Action A
}

// AllowedActions returns, in check order, the domain actions the request
// principal holds on the entity.
func AllowedActions[A any](ctx context.Context, s Spec, checker PermissionChecker, entityID string, checks []ActionCheck[A]) ([]A, error) {
	principal := auth.GetUser(ctx)
	actions := make([]A, 0, len(checks))
	for _, check := range checks {
		allowed, err := CheckPermissionForPrincipal(ctx, checker, entityID, check.Can, principal)
		if err != nil {
			return nil, errs.WithCause(errs.DependencyUnavailable("SpiceDB"), failurecause.WithAttrs(err, slog.String(s.IDColumn, entityID)))
		}
		if allowed {
			actions = append(actions, check.Action)
		}
	}
	return actions, nil
}

const lastOwnerConstraint = "must retain at least one durable Owner"

// MapMutationError gives a participant mutation failure that carries no
// Connect code its client answer. The database rejects removing the last
// Owner with the same wording the service uses.
func (s Spec) MapMutationError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	if strings.Contains(err.Error(), lastOwnerConstraint) {
		return errs.WithCause(errs.FailedPrecondition(s.Article+" "+lastOwnerConstraint), err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFoundMsg(s.Title + " participant not found")
	}
	return errs.Internal(err)
}
