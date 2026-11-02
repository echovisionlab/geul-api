//go:build integration

package pageaccesspostgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	pageaccesspostgres "github.com/echovisionlab/geul-api/internal/pageaccess/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func seedReader(t *testing.T, db *gorm.DB) *auth.UserInfo {
	t.Helper()
	identityID, memberID := uuid.NewString(), uuid.NewString()
	email := identityID + "@example.test"
	orytest.SeedKratosIdentityFixture(t, db, orytest.KratosIdentityFixture{ID: identityID, Email: email, Name: "Page reader"})
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identityID).Error)
	// The test soft-deletes this Member with the database clock.
	now := pgtest.DatabaseNow(t, db)
	require.NoError(t, db.Create(&model.Member{ID: memberID, AccountIdentityID: &identityID, Nickname: "Page reader " + memberID, Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now}).Error)
	return &auth.UserInfo{IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID), Authenticated: true, Onboarded: true}
}

func audienceRead[T any](t *testing.T, db *gorm.DB, read func(ctx context.Context, tx *gorm.DB) (T, error)) T {
	t.Helper()
	var value T
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		value, err = read(t.Context(), tx)
		return err
	}))
	return value
}

func TestAudienceReadsTagsAndSubscriptionsIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	reader := seedReader(t, db)
	tagA, tagB := uuid.NewString(), uuid.NewString()
	for _, id := range []string{tagA, tagB} {
		require.NoError(t, db.Create(&model.UserTag{ID: id, Name: "Access " + id}).Error)
	}
	hasTag := func(tags ...string) bool {
		return audienceRead(t, db, func(ctx context.Context, tx *gorm.DB) (bool, error) {
			return pageaccesspostgres.NewAudience(tx, identitystate.PostgresFence{}).HasUserTag(ctx, reader.MemberID.String(), tags)
		})
	}
	subscribed := func() bool {
		return audienceRead(t, db, func(ctx context.Context, tx *gorm.DB) (bool, error) {
			return pageaccesspostgres.NewAudience(tx, identitystate.PostgresFence{}).SubscribesToNewsletter(ctx, reader.IdentityID.String())
		})
	}
	require.False(t, hasTag(tagA, tagB))
	require.NoError(t, db.Create(&model.UserTagMapping{MemberID: reader.MemberID.String(), TagID: tagB}).Error)
	require.True(t, hasTag(tagA, tagB))
	require.False(t, hasTag(tagA))
	require.False(t, subscribed())
	require.NoError(t, db.Create(&model.NewsletterSubscription{IdentityID: reader.IdentityID.String(), SubscribedAt: time.Now().UTC()}).Error)
	require.True(t, subscribed())
}

// The identity fence a Page audience reads takes the database state of the
// Member and its Kratos Identity over the request principal.
func TestAudienceFencesTheStoredIdentityStateIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	reader := seedReader(t, db)
	active := func() bool {
		return audienceRead(t, db, func(ctx context.Context, tx *gorm.DB) (bool, error) {
			return pageaccesspostgres.NewAudience(tx, identitystate.PostgresFence{}).LockActivePrincipal(ctx, reader)
		})
	}
	require.True(t, active())
	identityID, memberID := reader.IdentityID.String(), reader.MemberID.String()
	require.NoError(t, db.Exec("UPDATE kratos.identities SET metadata_admin = jsonb_build_object('banned', true) WHERE id = ?::uuid", identityID).Error)
	require.False(t, active(), "a banned Identity")
	require.NoError(t, db.Exec("UPDATE kratos.identities SET metadata_admin = '{}'::jsonb, state = 'inactive' WHERE id = ?::uuid", identityID).Error)
	require.False(t, active(), "an inactive Identity")
	require.NoError(t, db.Exec("UPDATE kratos.identities SET state = 'active' WHERE id = ?::uuid", identityID).Error)
	require.NoError(t, db.Exec("UPDATE member SET account_identity_id = NULL, deleted_at = now(), social_links = '{}'::jsonb WHERE id = ?::uuid", memberID).Error)
	require.False(t, active(), "a deleted Member")
	require.NoError(t, db.Exec("UPDATE member SET account_identity_id = ?::uuid, deleted_at = NULL WHERE id = ?::uuid", identityID, memberID).Error)
	require.True(t, active())
	require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", identityID).Error)
	require.False(t, active(), "a deleted Identity")
}
