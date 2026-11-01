//go:build integration

package sharelink_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Share Link mutations check the identity fence and fresh authority inside
// their transaction. A principal whose Kratos Identity was deactivated or
// banned keeps a session and its SpiceDB grant until both catch up; the
// mutation masks the target as missing, like any caller without the action.
// The use-case tests pin the masking on fakes; this one keeps the fence's
// SQL over real Kratos rows and the SpiceDB admin grant.
func TestShareLinkMutationsRejectStaleIdentityIntegration(t *testing.T) {
	t.Parallel()
	for name, stale := range map[string]string{
		"deactivated identity": `UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`,
		"banned identity":      `UPDATE kratos.identities SET metadata_admin = '{"banned": true}'::jsonb WHERE id = ?::uuid`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stack := orytest.SetupOryStack(t)
			db := stack.DB
			adminID := uuid.NewString()
			orytest.SeedPostIntegrationIdentity(t, db, adminID, "Share Link Admin")
			orytest.GrantPostIntegrationRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
			ctx := orytest.NewAuditContext(t, adminID, orytest.PostIntegrationMemberID(adminID))
			labelID, documentID := uuid.NewString(), uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, documentID).Error)
			require.NoError(t, db.Exec(`INSERT INTO label (id, content_document_id) VALUES (?, ?)`, labelID, documentID).Error)
			// The administrator's authority reaches the Label through its policy.
			policy, err := policyv1.Label.TouchPolicy(labelID)
			require.NoError(t, err)
			removal, err := policyv1.Label.DeletePolicy(labelID)
			require.NoError(t, err)
			_, err = stack.SpiceDBClient.ApplyRelationshipsExpecting(t.Context(), []policyv1.RelationshipMutation{policy}, []policyv1.RelationshipMutation{removal})
			require.NoError(t, err)
			audit := &audittest.Recorder{}
			service, err := sharelink.NewService(sharelink.Deps{
				Units: sharelinkpostgres.NewUnits(db), SpiceDB: stack.SpiceDBClient, Fence: identitystate.PostgresFence{}, Audit: audit,
			})
			require.NoError(t, err)
			links := sharelink.NewConnectHandler(service)
			create := connect.NewRequest(&managev1.CreateShareLinkRequest{
				EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL, EntityId: labelID,
			})
			existing, err := links.CreateShareLink(ctx, create)
			require.NoError(t, err)
			require.NoError(t, db.Exec(stale, adminID).Error)

			_, err = links.CreateShareLink(ctx, create)
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			_, err = links.DeleteShareLink(ctx, connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: existing.Msg.ShareLink.Id}))
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

			var stored int64
			require.NoError(t, db.Model(&model.ShareLink{}).Where("entity_id = ?", labelID).Count(&stored).Error)
			require.EqualValues(t, 1, stored)
			require.Len(t, audit.Records(), 1)
		})
	}
}
