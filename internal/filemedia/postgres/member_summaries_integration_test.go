//go:build integration

package filemediapostgres_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediamembers"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestMemberSummaryPoolPortPreservesAvatarsAndHidesTombstoneAvatarsIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true, BootstrapKratosStub: true}).DB
	active, identity, deleted, missing := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	email := identity + "@example.test"
	orytest.SeedKratosIdentityFixture(t, db, orytest.KratosIdentityFixture{ID: identity, Email: email, Name: "Live Member", CreatedAt: now})
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", active, identity).Error)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identity).Error)
	require.NoError(t, db.Create(&model.Member{ID: active, AccountIdentityID: &identity, Nickname: "live_member", Onboarded: true, PrimaryEmail: &email, AvailableEmails: pq.StringArray{email}}).Error)
	require.NoError(t, db.Create(&model.Member{ID: deleted, Nickname: deleted, DeletedAt: &now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now}).Error)
	activeAsset := ""
	for _, memberID := range []string{active, deleted} {
		asset := uuid.NewString()
		require.NoError(t, db.Create(&model.PublicAsset{ID: asset, Kind: "avatar", Extension: "webp", MimeType: "image/webp", ObjectKey: "asset/" + asset + ".webp", Disposition: "inline", Status: model.PublicAssetStatusReady, FileSize: new(int64(64)), SHA256: bytes.Repeat([]byte{9}, 32), ReadyAt: &now, CreatedAt: now, UpdatedAt: now}).Error)
		require.NoError(t, db.Create(&model.PublicAssetBinding{AssetID: asset, OwnerType: "member", OwnerID: memberID, BindingKey: "avatar"}).Error)
		if memberID == active {
			activeAsset = asset
		}
	}
	result, err := filemediamembers.NewMemberSummaries(db, "https://cdn.example.com").Load(t.Context(), []string{active, deleted, missing})
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, domain.MemberSummary{ID: active, Nickname: "live_member", Avatar: &mediaasset.Reference{AssetID: activeAsset, URL: "https://cdn.example.com/asset/" + activeAsset + "/avatar.webp", Extension: "webp", MimeType: "image/webp", FileSize: 64, SHA256: bytes.Repeat([]byte{9}, 32), Disposition: mediaasset.DispositionInline}}, result[active])
	require.Equal(t, domain.MemberSummary{ID: deleted, Nickname: deleted, Deleted: true}, result[deleted])
	require.NotContains(t, result, missing)
}
