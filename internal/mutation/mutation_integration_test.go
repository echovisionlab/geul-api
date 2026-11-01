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

// The relationship batch commits with the domain change, and a later step's
// failure compensates it before the rollback, so SpiceDB and PostgreSQL agree
// either way.
func TestRunWritesRelationshipsWithTheChangeIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.40")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), admin.AuthUserInfo())

	run := func(recorder *audittest.Recorder) (string, error) {
		runner, err := NewRunner(stack.DB, stack.SpiceDBClient, identitystate.PostgresFence{}, recorder)
		require.NoError(t, err)
		resourceID := uuid.NewString()
		_, err = Run(ctx, runner, Spec[model.UserTag]{
			Access: FreshAdminCan(policyv1.Platform.IsAdmin()),
			Change: func(_ context.Context, tx *gorm.DB) (model.UserTag, error) {
				tag := model.UserTag{Name: "pipeline-" + resourceID}
				return tag, tx.Create(&tag).Error
			},
			Audit: func(tag model.UserTag) Audit {
				return RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, tag.ID, tag.Name)
				})
			},
			Relationships: func(model.UserTag) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
				touch, err := policyv1.Campaign.TouchPolicy(resourceID)
				if err != nil {
					return nil, nil, err
				}
				remove, err := policyv1.Campaign.DeletePolicy(resourceID)
				return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, err
			},
		})
		return resourceID, err
	}
	canManage := func(resourceID string) bool {
		can, err := policyv1.Campaign.Manage(resourceID)
		require.NoError(t, err)
		decision, err := auth.AuthorizationDecision(ctx, can)
		require.NoError(t, err)
		allowed, err := stack.SpiceDBClient.Can(ctx, decision)
		require.NoError(t, err)
		return allowed
	}
	tagCount := func(resourceID string) int64 {
		var count int64
		require.NoError(t, stack.DB.Model(&model.UserTag{}).Where("name = ?", "pipeline-"+resourceID).Count(&count).Error)
		return count
	}

	committed, err := run(&audittest.Recorder{})
	require.NoError(t, err)
	require.True(t, canManage(committed))
	require.EqualValues(t, 1, tagCount(committed))

	auditDown := errors.New("audit unavailable")
	rolledBack, err := run(&audittest.Recorder{Err: auditDown})
	require.ErrorIs(t, err, auditDown)
	require.False(t, canManage(rolledBack))
	require.Zero(t, tagCount(rolledBack))
}
