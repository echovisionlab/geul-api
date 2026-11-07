//go:build integration

package artist_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// labelAuthorityFixture is an Artist, and the people of the Labels it may join.
type labelAuthorityFixture struct {
	t       *testing.T
	db      *gorm.DB
	service artistIntegrationHandler
	admin   context.Context
	artist  string
}

func newLabelAuthorityFixture(t *testing.T) labelAuthorityFixture {
	t.Helper()
	db := orytest.NewIntegrationDB(t)
	adminID := orytest.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Label Authority Admin")
	service := newArtistIntegrationSpiceDBService(t, db, adminID)
	admin := artistIntegrationAdminCtx(adminID)
	created, err := service.CreateArtist(admin, connect.NewRequest(&managev1.CreateArtistRequest{
		Name: "Signed Artist", Document: artistContentDocument("en", "Signed Artist"),
	}))
	require.NoError(t, err)
	return labelAuthorityFixture{t: t, db: db, service: service, admin: admin, artist: created.Msg.Id}
}

func (f labelAuthorityFixture) person(name string) string {
	f.t.Helper()
	id := orytest.IntegrationUUID()
	seedArtistIdentity(f.t, f.db, id, name)
	return id
}

// seedLabel writes a Label and its relationships the way the Label service does.
func (f labelAuthorityFixture) seedLabel(owner string, managers ...string) string {
	f.t.Helper()
	id, document := orytest.IntegrationUUID(), orytest.IntegrationUUID()
	require.NoError(f.t, f.db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, document).Error)
	require.NoError(f.t, f.db.Exec(`INSERT INTO label (id, status, source_locale, content_document_id) VALUES (?, 'LABEL_STATUS_PUBLISHED', 'en', ?)`, id, document).Error)
	policy, err := policyv1.Label.TouchPolicy(id)
	require.NoError(f.t, err)
	mutations := []policyv1.RelationshipMutation{policy}
	ownerActor, err := policyv1.NewAccountIdentityActor(owner)
	require.NoError(f.t, err)
	ownerTouch, err := policyv1.Label.TouchOwner(id, ownerActor)
	require.NoError(f.t, err)
	mutations = append(mutations, ownerTouch)
	for _, manager := range managers {
		actor, err := policyv1.NewAccountIdentityActor(manager)
		require.NoError(f.t, err)
		touch, err := policyv1.Label.TouchManager(id, actor)
		require.NoError(f.t, err)
		mutations = append(mutations, touch)
	}
	_, err = f.service.spiceDB.ApplyRelationships(f.t.Context(), mutations...)
	require.NoError(f.t, err)
	return id
}

func (f labelAuthorityFixture) setLabels(ctx context.Context, labelIDs ...string) error {
	f.t.Helper()
	_, err := f.service.SetArtistLabels(ctx, connect.NewRequest(&managev1.SetArtistLabelsRequest{ArtistId: f.artist, LabelIds: labelIDs}))
	return err
}

func (f labelAuthorityFixture) may(identityID string, action func(string) (policyv1.Can, error)) bool {
	f.t.Helper()
	can, err := action(f.artist)
	require.NoError(f.t, err)
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(f.t, err)
	allowed, err := f.service.spiceDB.CheckActorCan(f.t.Context(), actor, can)
	require.NoError(f.t, err)
	return allowed
}

func TestALabelsPeopleHoldItsAuthorityOverItsOnlyArtistIntegration(t *testing.T) {
	t.Parallel()
	f := newLabelAuthorityFixture(t)
	owner, manager := f.person("Label Owner"), f.person("Label Manager")
	label := f.seedLabel(owner, manager)
	require.False(t, f.may(owner, policyv1.Artist.Edit), "no authority before the Artist joins")

	require.NoError(t, f.setLabels(f.admin, label))

	require.True(t, f.may(owner, policyv1.Artist.Edit))
	require.True(t, f.may(owner, policyv1.Artist.Publish))
	require.False(t, f.may(owner, policyv1.Artist.Delete), "a Label does not take the Artist's own powers")
	require.False(t, f.may(owner, policyv1.Artist.ManageParticipants))
	require.True(t, f.may(manager, policyv1.Artist.Edit))
	require.False(t, f.may(manager, policyv1.Artist.Publish), "managing a Label does not publish it either")

	require.NoError(t, f.setLabels(f.admin))
	require.False(t, f.may(owner, policyv1.Artist.Edit), "leaving the Label ends its authority")
}

func TestAnArtistInSeveralLabelsAnswersOnlyToWhoeverHoldsThemAllIntegration(t *testing.T) {
	t.Parallel()
	f := newLabelAuthorityFixture(t)
	first, second, both := f.person("First Owner"), f.person("Second Owner"), f.person("Owner Of Both")
	firstLabel, secondLabel := f.seedLabel(first), f.seedLabel(second)
	actor, err := policyv1.NewAccountIdentityActor(both)
	require.NoError(t, err)
	for _, label := range []string{firstLabel, secondLabel} {
		touch, err := policyv1.Label.TouchOwner(label, actor)
		require.NoError(t, err)
		_, err = f.service.spiceDB.ApplyRelationships(t.Context(), touch)
		require.NoError(t, err)
	}

	require.NoError(t, f.setLabels(f.admin, firstLabel, secondLabel))

	require.False(t, f.may(first, policyv1.Artist.Edit))
	require.False(t, f.may(first, policyv1.Artist.Publish))
	require.False(t, f.may(second, policyv1.Artist.Edit))
	require.True(t, f.may(both, policyv1.Artist.Publish))
}

func TestOnlySomeoneWhoManagesBothSidesMayLinkAnArtistToALabelIntegration(t *testing.T) {
	t.Parallel()
	f := newLabelAuthorityFixture(t)
	owner := f.person("Label Owner")
	label := f.seedLabel(owner)

	err := f.setLabels(artistIntegrationAdminCtx(owner), label)

	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "a Label owner cannot claim an Artist")
	require.False(t, f.may(owner, policyv1.Artist.Edit))
}

// An Artist's deletion takes its Label relationships with it, and the batch would
// fail if the snapshot held a relationship the deletion could not express.
func TestDeletingAnArtistRemovesItsLabelRelationshipsIntegration(t *testing.T) {
	t.Parallel()
	f := newLabelAuthorityFixture(t)
	owner := f.person("Label Owner")
	require.NoError(t, f.setLabels(f.admin, f.seedLabel(owner)))
	preview, err := f.service.PreviewDeleteArtist(f.admin, connect.NewRequest(&managev1.PreviewDeleteArtistRequest{Id: f.artist}))
	require.NoError(t, err)

	_, err = f.service.DeleteArtist(f.admin, connect.NewRequest(&managev1.DeleteArtistRequest{Id: f.artist, ExpectedRevision: preview.Msg.Revision}))

	require.NoError(t, err)
	require.False(t, f.may(owner, policyv1.Artist.Edit))
}
