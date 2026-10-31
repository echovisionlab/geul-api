//go:build integration

package sharelink_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/artistlabelpublic"
	"github.com/echovisionlab/geul-api/internal/testutil/sharelinktest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Share Link mutations check the identity fence and fresh authority inside
// their transaction. A principal whose Identity was deactivated or banned
// keeps a session and its SpiceDB grant until both catch up; the mutation
// masks the target as missing, like any caller without the action.
func TestShareLinkMutationsRejectStaleIdentityIntegration(t *testing.T) {
	t.Parallel()
	fixture := artistlabelpublic.New(t)
	ctx := testutil.NewAuditContext(t, fixture.AdminID, testutil.PostIntegrationMemberID(fixture.AdminID))
	links := sharelinktest.NewHandler(t, fixture.DB, fixture.SpiceDB)
	existing, err := links.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
		EntityId:   fixture.MainLabel.Id,
	}))
	require.NoError(t, err)
	audits := labelAuditCount(t, fixture.DB, fixture.MainLabel.Id)

	for name, stale := range map[string]string{
		"deactivated identity": `UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`,
		"banned identity":      `UPDATE kratos.identities SET state = 'active', metadata_admin = '{"banned": true}'::jsonb WHERE id = ?::uuid`,
	} {
		require.NoError(t, fixture.DB.Exec(stale, fixture.AdminID).Error, name)

		_, err = links.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
			EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
			EntityId:   fixture.MainLabel.Id,
		}))
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), name)
		_, err = links.DeleteShareLink(ctx, connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: existing.Msg.ShareLink.Id}))
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), name)

		var stored int64
		require.NoError(t, fixture.DB.Model(&model.ShareLink{}).Where("entity_id = ?", fixture.MainLabel.Id).Count(&stored).Error)
		require.EqualValues(t, 1, stored, name)
		require.Equal(t, audits, labelAuditCount(t, fixture.DB, fixture.MainLabel.Id), name)
	}
}

func labelAuditCount(t *testing.T, db *gorm.DB, labelID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("domain_audit").Where("target_type = ? AND target_id = ?", "label", labelID).Count(&count).Error)
	return count
}
