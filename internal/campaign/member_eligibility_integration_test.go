//go:build integration

package campaign

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUnonboardedMemberIsExcludedFromCampaignRecipientConsumersIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, "user")
	require.NoError(t, db.Exec(
		`UPDATE member SET onboarded=FALSE WHERE id=?::uuid`, user.MemberID,
	).Error)
	tag := model.UserTag{ID: uuid.NewString(), Name: "mail-target-" + uuid.NewString()}
	require.NoError(t, db.Create(&tag).Error)
	require.NoError(t, db.Create(&model.UserTagMapping{
		MemberID: user.MemberID,
		TagID:    tag.ID,
	}).Error)
	selection := &bulkEmailRecipientSelection{
		Mode:         CampaignDeliveryTargetModeUserTags,
		MemberTagIDs: []string{tag.ID},
	}

	count, err := countBulkEmailRecipients(t.Context(), db, stack.SpiceDBClient, selection)
	require.NoError(t, err)
	require.Zero(t, count)

	eligible, err := authorizationtarget.EligibleMemberIDs(
		t.Context(), db, []string{user.MemberID},
	)
	require.NoError(t, err)
	require.Empty(t, eligible)
	_, err = resolveCampaignDeliveryExcludedMemberPairs(
		t.Context(), db, uuid.NewString(), []string{user.MemberID},
	)
	require.ErrorContains(t, err, "must be onboarded")

	require.NoError(t, db.Exec(
		`UPDATE member SET onboarded=TRUE WHERE id=?::uuid`, user.MemberID,
	).Error)
	count, err = countBulkEmailRecipients(t.Context(), db, stack.SpiceDBClient, selection)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}
