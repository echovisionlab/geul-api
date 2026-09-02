//go:build integration

package identitystate

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func trySessionAdvisoryLock(
	t *testing.T,
	db *gorm.DB,
	lockQuery string,
	unlockQuery string,
	args ...any,
) bool {
	t.Helper()
	var acquired bool
	require.NoError(t, db.Connection(func(connection *gorm.DB) error {
		if err := connection.Raw(lockQuery, args...).Scan(&acquired).Error; err != nil {
			return err
		}
		if !acquired {
			return nil
		}
		var released bool
		if err := connection.Raw(unlockQuery, args...).Scan(&released).Error; err != nil {
			return err
		}
		if !released {
			return fmt.Errorf("probe advisory lock was not released")
		}
		return nil
	}))
	return acquired
}

func acquireAuthorizationMutationTransactionWithTimeout(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if err := tx.Exec("SET LOCAL lock_timeout = '100ms'").Error; err != nil {
		return errors.Join(err, tx.Rollback().Error)
	}
	lockErr := authzmutation.LockTransaction(tx)
	return errors.Join(lockErr, tx.Rollback().Error)
}

func TestUserIdentityStateMutationRetainsLeaseTransactionIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	require.NoError(t, WithMutation(t.Context(), stack.DB, user.IdentityID, func(_ context.Context, tx *gorm.DB) error {
		var count int64
		if err := tx.Table("member").Where("id = ?::uuid", user.MemberID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("lease fixture is not visible through identity-state mutation transaction")
		}
		return nil
	}))
}

func TestUserIdentityStateMutationAcquiresGlobalBeforeIdentitySessionFenceIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())

	identityHeld := make(chan struct{})
	releaseIdentity := make(chan struct{})
	identityBlockerDone := make(chan error, 1)
	go func() {
		identityBlockerDone <- stack.DB.WithContext(t.Context()).Connection(func(connection *gorm.DB) (returnErr error) {
			if err := connection.Exec(
				"SELECT pg_advisory_lock(hashtextextended(?, ?))",
				user.IdentityID,
				identityStateFenceSeed,
			).Error; err != nil {
				return err
			}
			defer func() {
				var released bool
				returnErr = errors.Join(returnErr, connection.Raw(
					"SELECT pg_advisory_unlock(hashtextextended(?, ?))",
					user.IdentityID,
					identityStateFenceSeed,
				).Scan(&released).Error)
				if !released {
					returnErr = errors.Join(returnErr, fmt.Errorf("identity blocker lock was not released"))
				}
			}()
			close(identityHeld)
			select {
			case <-releaseIdentity:
				return nil
			case <-t.Context().Done():
				return t.Context().Err()
			}
		})
	}()
	select {
	case <-identityHeld:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out acquiring identity blocker lock")
	}

	mutationEntered := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- WithMutation(t.Context(), stack.DB, user.IdentityID, func(_ context.Context, _ *gorm.DB) error {
			close(mutationEntered)
			select {
			case <-releaseMutation:
				return nil
			case <-t.Context().Done():
				return t.Context().Err()
			}
		})
	}()

	globalLockHeld := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		globalLockHeld = !trySessionAdvisoryLock(
			t,
			stack.DB,
			"SELECT pg_try_advisory_lock(?)",
			"SELECT pg_advisory_unlock(?)",
			authzmutation.GlobalLockKey,
		)
		if globalLockHeld {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, globalLockHeld, "global authorization lock must be acquired before waiting for the identity lock")

	close(releaseIdentity)
	select {
	case err := <-identityBlockerDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out releasing identity blocker lock")
	}
	select {
	case <-mutationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out entering identity mutation")
	}

	require.False(t, trySessionAdvisoryLock(
		t,
		stack.DB,
		"SELECT pg_try_advisory_lock(?)",
		"SELECT pg_advisory_unlock(?)",
		authzmutation.GlobalLockKey,
	))
	require.False(t, trySessionAdvisoryLock(
		t,
		stack.DB,
		"SELECT pg_try_advisory_lock(hashtextextended(?, ?))",
		"SELECT pg_advisory_unlock(hashtextextended(?, ?))",
		user.IdentityID,
		identityStateFenceSeed,
	))
	require.ErrorContains(
		t,
		acquireAuthorizationMutationTransactionWithTimeout(stack.DB),
		"lock timeout",
		"a synchronous authorization transaction must contend with the lifecycle session lock",
	)

	close(releaseMutation)
	select {
	case err := <-mutationDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out releasing identity mutation locks")
	}

	require.True(t, trySessionAdvisoryLock(
		t,
		stack.DB,
		"SELECT pg_try_advisory_lock(?)",
		"SELECT pg_advisory_unlock(?)",
		authzmutation.GlobalLockKey,
	))
	require.True(t, trySessionAdvisoryLock(
		t,
		stack.DB,
		"SELECT pg_try_advisory_lock(hashtextextended(?, ?))",
		"SELECT pg_advisory_unlock(hashtextextended(?, ?))",
		user.IdentityID,
		identityStateFenceSeed,
	))
	require.NoError(t, acquireAuthorizationMutationTransactionWithTimeout(stack.DB))
}
