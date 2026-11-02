//go:build integration

package memberpostgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
)

func TestDeletedMemberNicknameCanBeReusedWhileActiveClaimsStayUniqueIntegration(t *testing.T) {
	t.Parallel()
	db := emailDB(t)
	nickname := "reusable-" + uuid.NewString()
	retiredID := insertDeletedMemberEmailFixture(t, db, time.Now().UTC().Add(-24*time.Hour), []string{"retired@example.test"})
	require.NoError(t, db.Exec("UPDATE member SET nickname = ? WHERE id = ?::uuid", nickname, retiredID).Error)
	activeID, _ := seedLinked(t, db, "active-"+uuid.NewString(), true)
	otherID, _ := seedLinked(t, db, "other-"+uuid.NewString(), true)
	store := memberpostgres.NewStore(db)
	require.NoError(t, store.UpdateProfile(t.Context(), activeID, member.ProfileUpdate{Nickname: &nickname, UpdatedAt: time.Now().UTC()}))
	require.ErrorIs(t, store.UpdateProfile(t.Context(), otherID, member.ProfileUpdate{Nickname: &nickname, UpdatedAt: time.Now().UTC()}), member.ErrNicknameTaken)
	var stored string
	require.NoError(t, db.Raw("SELECT nickname FROM member WHERE id = ?::uuid", activeID).Scan(&stored).Error)
	require.Equal(t, nickname, stored)
}
