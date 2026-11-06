//go:build integration

package mutation

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Two runners stand for two domains: each opens its own transaction unless a
// shared one is on the context.
func TestSharedTransactionCommitsOrRollsBackEveryJoiningMutationIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.41")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), admin.AuthUserInfo())

	newRunner := func() *Runner {
		runner, err := NewRunner(stack.DB, stack.SpiceDBClient, identitystate.PostgresFence{}, &audittest.Recorder{})
		require.NoError(t, err)
		return runner
	}
	insert := func(ctx context.Context, runner *Runner, name string) error {
		_, err := Run(ctx, runner, Spec[model.UserTag]{
			Access: FreshAdminCan(policyv1.Platform.IsAdmin()),
			Change: func(_ context.Context, tx *gorm.DB) (model.UserTag, error) {
				tag := model.UserTag{Name: name}
				return tag, tx.Create(&tag).Error
			},
			Audit: func(model.UserTag) Audit { return Unchanged() },
		})
		return err
	}
	exists := func(name string) bool {
		var count int64
		require.NoError(t, stack.DB.WithContext(ctx).Model(&model.UserTag{}).Where("name = ?", name).Count(&count).Error)
		return count == 1
	}

	t.Run("both commit together, and a read sees what the transaction wrote", func(t *testing.T) {
		first, second := "shared-"+uuid.NewString(), "shared-"+uuid.NewString()
		committed := false
		require.NoError(t, InSharedTransaction(ctx, stack.DB, func(ctx context.Context) error {
			require.NoError(t, insert(ctx, newRunner(), first))
			require.False(t, exists(first), "another connection must not see an uncommitted write")
			var seen int64
			require.NoError(t, Read(ctx, stack.DB, func(tx *gorm.DB) error {
				return tx.Model(&model.UserTag{}).Where("name = ?", first).Count(&seen).Error
			}))
			require.EqualValues(t, 1, seen, "a read under the shared transaction must see its writes")
			AfterCommit(ctx, func() { committed = exists(first) && exists(second) })
			return insert(ctx, newRunner(), second)
		}))
		require.True(t, exists(first))
		require.True(t, exists(second))
		require.True(t, committed, "after-commit work must run once both writes are visible")
	})

	t.Run("a later failure undoes an earlier mutation that already finished", func(t *testing.T) {
		first := "shared-" + uuid.NewString()
		failure := errors.New("the second domain refused")
		afterCommit := false
		err := InSharedTransaction(ctx, stack.DB, func(ctx context.Context) error {
			require.NoError(t, insert(ctx, newRunner(), first))
			AfterCommit(ctx, func() { afterCommit = true })
			return failure
		})
		require.ErrorIs(t, err, failure)
		require.False(t, exists(first))
		require.False(t, afterCommit, "after-commit work must not run for a rollback")
	})

	t.Run("a mutation that fails alone rolls back only to its savepoint", func(t *testing.T) {
		kept, refused := "shared-"+uuid.NewString(), "shared-"+uuid.NewString()
		require.NoError(t, InSharedTransaction(ctx, stack.DB, func(ctx context.Context) error {
			require.NoError(t, insert(ctx, newRunner(), kept))
			_, err := Run(ctx, newRunner(), Spec[model.UserTag]{
				Access: FreshAdminCan(policyv1.Platform.IsAdmin()),
				Change: func(_ context.Context, tx *gorm.DB) (model.UserTag, error) {
					tag := model.UserTag{Name: refused}
					if err := tx.Create(&tag).Error; err != nil {
						return tag, err
					}
					return tag, errors.New("validation only")
				},
				Audit: func(model.UserTag) Audit { return Unchanged() },
			})
			require.Error(t, err)
			return nil
		}))
		require.True(t, exists(kept))
		require.False(t, exists(refused))
	})

	t.Run("a relationship mutation refuses to join", func(t *testing.T) {
		err := InSharedTransaction(ctx, stack.DB, func(ctx context.Context) error {
			_, err := Run(ctx, newRunner(), Spec[model.UserTag]{
				Access: FreshAdminCan(policyv1.Platform.IsAdmin()),
				Change: func(context.Context, *gorm.DB) (model.UserTag, error) { return model.UserTag{}, nil },
				Audit:  func(model.UserTag) Audit { return Unchanged() },
				Relationships: func(model.UserTag) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
					return nil, nil, nil
				},
			})
			return err
		})
		require.ErrorContains(t, err, "cannot join")
	})

	t.Run("shared transactions do not nest", func(t *testing.T) {
		err := InSharedTransaction(ctx, stack.DB, func(ctx context.Context) error {
			return InSharedTransaction(ctx, stack.DB, func(context.Context) error { return nil })
		})
		require.ErrorContains(t, err, "do not nest")
	})
}
