//go:build integration

package sharelink_test

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	sharelinkdomain "github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/artistlabelpublic"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestArtistAndLabelShareLinkAuditTargetsDoNotCrossIntegration(t *testing.T) {
	fixture := artistlabelpublic.New(t)
	ctx := testutil.NewAuditContext(t, fixture.AdminID, testutil.PostIntegrationMemberID(fixture.AdminID))
	links := sharelinkdomain.NewService(
		fixture.DB,
		sharelinkadapter.NewAuthority(
			fixture.DB,
			fixture.SpiceDB,
			telemetry.NewDurableWriter(fixture.DB),
		),
	)

	artistLink, err := links.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST,
		EntityId:   fixture.MainArtist.Id,
	}))
	require.NoError(t, err)
	labelLink, err := links.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
		EntityId:   fixture.MainLabel.Id,
	}))
	require.NoError(t, err)
	_, err = links.DeleteShareLink(ctx, connect.NewRequest(&managev1.DeleteShareLinkRequest{
		Id: artistLink.Msg.ShareLink.Id,
	}))
	require.NoError(t, err)
	_, err = links.DeleteShareLink(ctx, connect.NewRequest(&managev1.DeleteShareLinkRequest{
		Id: labelLink.Msg.ShareLink.Id,
	}))
	require.NoError(t, err)

	artistRows := shareLinkAuditRows(t, fixture.DB, "artist", fixture.MainArtist.Id)
	require.Len(t, artistRows, 2)
	require.Equal(t, "artist.updated", artistRows[0].Action)
	require.Equal(t, "artist.updated", artistRows[1].Action)
	requireShareLinkAuditOperation(t, artistRows[0].Attributes, "created")
	requireShareLinkAuditOperation(t, artistRows[1].Attributes, "deleted")

	labelRows := shareLinkAuditRows(t, fixture.DB, "label", fixture.MainLabel.Id)
	require.Len(t, labelRows, 2)
	require.Equal(t, "label.updated", labelRows[0].Action)
	require.Equal(t, "label.updated", labelRows[1].Action)
	requireShareLinkAuditOperation(t, labelRows[0].Attributes, "created")
	requireShareLinkAuditOperation(t, labelRows[1].Attributes, "deleted")

	var crossed int64
	require.NoError(t, fixture.DB.Table("domain_audit").Where(
		"(target_type = ? AND target_id = ?) OR (target_type = ? AND target_id = ?)",
		"artist", fixture.MainLabel.Id,
		"label", fixture.MainArtist.Id,
	).Count(&crossed).Error)
	require.Zero(t, crossed)
}

type shareLinkAuditRow struct {
	Action     string
	Attributes []byte
}

func requireShareLinkAuditOperation(t *testing.T, attributes []byte, operation string) {
	t.Helper()
	var decoded struct {
		ItemOperation string `json:"item_operation"`
	}
	require.NoError(t, json.Unmarshal(attributes, &decoded))
	require.Equal(t, operation, decoded.ItemOperation)
}

func shareLinkAuditRows(t *testing.T, db *gorm.DB, targetType, targetID string) []shareLinkAuditRow {
	t.Helper()
	var rows []shareLinkAuditRow
	require.NoError(t, db.Raw(`
		SELECT action, attributes
		FROM domain_audit
		WHERE target_type = ? AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, targetType, targetID).Scan(&rows).Error)
	return rows
}
