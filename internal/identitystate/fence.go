package identitystate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	identityStateFenceSeed = int64(0x4745554c55534552) // GEULUSER
	// MutationTimeout bounds one lifecycle mutation across database and external authority calls.
	MutationTimeout = 15 * time.Second
	unlockTimeout   = 2 * time.Second
)

// Lock serializes lifecycle and authority changes for one Identity.
func Lock(tx *gorm.DB, identityID string) error {
	if tx == nil {
		return fmt.Errorf("user identity state fence requires a transaction")
	}
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return fmt.Errorf("user identity state fence requires user id")
	}
	return tx.Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?, ?))",
		identityID,
		identityStateFenceSeed,
	).Error
}

// WithMutation holds the global authorization lock and one Identity lifecycle
// fence across database, Identity provider, and SpiceDB work.
func WithMutation(
	ctx context.Context,
	db *gorm.DB,
	identityID string,
	work func(context.Context, *gorm.DB) error,
) error {
	if db == nil {
		return fmt.Errorf("user identity state mutation requires database")
	}
	if work == nil {
		return fmt.Errorf("user identity state mutation work is required")
	}
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return fmt.Errorf("user identity state fence requires user id")
	}
	if db.Statement != nil {
		if _, ok := db.Statement.ConnPool.(gorm.TxCommitter); ok {
			mutationCtx, cancel := context.WithTimeout(ctx, MutationTimeout)
			defer cancel()
			connection := db.WithContext(mutationCtx)
			if err := authzmutation.LockTransaction(connection); err != nil {
				return err
			}
			if err := Lock(connection, identityID); err != nil {
				return err
			}
			return work(mutationCtx, connection)
		}
	}
	return db.WithContext(ctx).Connection(func(connection *gorm.DB) (returnErr error) {
		// Match authzmutation.Execute's global -> identity order and keep
		// both session locks across external identity and SpiceDB work.
		if err := authzmutation.LockSession(connection); err != nil {
			return err
		}
		defer func() {
			returnErr = errors.Join(returnErr, authzmutation.ReleaseSession(ctx, connection))
		}()
		if err := connection.Exec(
			"SELECT pg_advisory_lock(hashtextextended(?, ?))",
			identityID,
			identityStateFenceSeed,
		).Error; err != nil {
			return err
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
			defer cancel()
			var released bool
			unlockErr := connection.WithContext(unlockCtx).Raw(
				"SELECT pg_advisory_unlock(hashtextextended(?, ?))",
				identityID,
				identityStateFenceSeed,
			).Scan(&released).Error
			if unlockErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("release user identity state fence: %w", unlockErr))
			} else if !released {
				returnErr = errors.Join(returnErr, fmt.Errorf("release user identity state fence: lock was not held"))
			}
		}()
		mutationCtx, cancel := context.WithTimeout(ctx, MutationTimeout)
		defer cancel()
		return work(mutationCtx, connection.WithContext(mutationCtx))
	})
}

// LockActivePrincipal locks the exact Member and Identity pair and reports
// whether it remains active, onboarded, and unbanned inside the transaction.
func LockActivePrincipal(ctx context.Context, tx *gorm.DB, principal *auth.UserInfo) (bool, error) {
	if tx == nil || principal == nil || strings.TrimSpace(principal.MemberID.String()) == "" || strings.TrimSpace(principal.IdentityID.String()) == "" {
		return false, nil
	}
	if err := Lock(tx, principal.IdentityID.String()); err != nil {
		return false, err
	}
	var member struct {
		ID        string `gorm:"column:id"`
		Onboarded bool   `gorm:"column:onboarded"`
		Active    bool   `gorm:"column:active"`
	}
	result := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id, onboarded, deleted_at IS NULL AS active").
		Where("id = ? AND account_identity_id = ?", principal.MemberID.String(), principal.IdentityID.String()).
		Take(&member)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if result.Error != nil {
		return false, result.Error
	}
	var identity struct {
		ID     string `gorm:"column:id"`
		State  string `gorm:"column:state"`
		Banned bool   `gorm:"column:banned"`
	}
	result = tx.WithContext(ctx).
		Table("kratos.identities").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id, state, LOWER(COALESCE(metadata_admin ->> 'banned', 'false')) IN ('true', '1') AS banned").
		Where("id = ? AND external_id = ?", principal.IdentityID.String(), principal.MemberID.String()).
		Take(&identity)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if result.Error != nil {
		return false, result.Error
	}
	return member.Active && member.Onboarded && identity.State == auth.KratosStateActive && !identity.Banned, nil
}

// RequireFreshCan locks the authenticated principal lifecycle and checks one
// generated domain action with fully-consistent authorization before a domain
// mutation. The caller owns the action; this lifecycle fence does not replace
// it with a generic platform role check.
func RequireFreshCan(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB authz.AuthorizationDecisionChecker,
	can policyv1.Can,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	active, err := LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if !active {
		if can.Valid() {
			return errs.NoPermission(can.Action().Name(), can.Resource().Type())
		}
		return errs.NoPermission("access", "resource")
	}
	return authz.RequireCan(ctx, spiceDB, can)
}

// RequireFreshAdminCan preserves the AdminRequired denial contract for an
// admin-only domain mutation while using the common exact-Can lifecycle seam.
func RequireFreshAdminCan(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB authz.AuthorizationDecisionChecker,
	can policyv1.Can,
) error {
	err := RequireFreshCan(ctx, tx, spiceDB, can)
	if connect.CodeOf(err) == connect.CodePermissionDenied {
		return errs.AdminRequired()
	}
	return err
}
