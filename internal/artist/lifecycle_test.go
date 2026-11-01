package artist_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/artist/artisttest"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestCreateArtistStoresTheArtistAndMakesTheCallerItsOwner(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	parent := h.seedArtist(t, "Parent")
	slug := "new-artist"

	view, err := h.service.CreateArtist(caller.context(t), artist.CreateArtistCommand{
		Name: " New Artist ", Slug: &slug, RealName: stringPointer("Real Name"), ParentArtistID: &parent.ID,
		SocialLinks: map[string]string{"site": "https://artist.example"},
	})

	require.NoError(t, err)
	require.Equal(t, "New Artist", view.Name)
	require.Equal(t, "Real Name", *view.Artist.RealName)
	require.Equal(t, artist.StatusDraft, view.Artist.Status)
	require.Equal(t, "en", view.Artist.SourceLocale)
	require.Equal(t, parent.ID, *view.Artist.ParentArtistID)
	require.NotEmpty(t, view.Revision)
	require.NotNil(t, view.Artist.UpdatedAt, "the source title save touches the new row")
	require.Equal(t, map[string]string{caller.memberID: entityparticipant.RoleOwner}, h.store.Participants(view.Artist.ID))
	title, ok := h.store.Locale(view.Artist.ID, "en")
	require.True(t, ok)
	require.Equal(t, "New Artist", *title.Title)
	require.Equal(t, []artisttest.OGRequest{{ArtistID: view.Artist.ID, Reason: "artist_created"}}, h.store.OGRequests())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditArtistCreated}, auditActions(h.audit))
	policy, err := policyv1.Artist.TouchPolicy(view.Artist.ID)
	require.NoError(t, err)
	owner, err := policyv1.NewAccountIdentityActor(caller.identityID)
	require.NoError(t, err)
	ownerTouch, err := policyv1.Artist.TouchOwner(view.Artist.ID, owner)
	require.NoError(t, err)
	parentTouch, err := policyv1.Artist.TouchParent(view.Artist.ID, parent.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{policy, ownerTouch, parentTouch}}, h.checker.Applied())
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit,
		mutation.StepRelationships, mutation.StepCommit,
	}, h.units.Steps()[:6])
	require.Equal(t, 1, h.store.ParentGraphLocks(), "the parent is validated under the hierarchy lock")
}

func TestCreateArtistRejectsBeforeStoringAnything(t *testing.T) {
	create, err := policyv1.Artist.Create()
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		prepare func(*harness) artist.CreateArtistCommand
		code    connect.Code
		message string
	}{
		{
			name:    "blank name",
			prepare: func(*harness) artist.CreateArtistCommand { return artist.CreateArtistCommand{Name: "  "} },
			code:    connect.CodeInvalidArgument, message: "name is required",
		},
		{
			name: "taken slug",
			prepare: func(h *harness) artist.CreateArtistCommand {
				h.seedArtist(t, "Taken", func(value *artist.Artist) { value.Slug = stringPointer("taken") })
				return artist.CreateArtistCommand{Name: "New", Slug: stringPointer("taken")}
			},
			code: connect.CodeAlreadyExists, message: "artist with slug 'taken' already exists",
		},
		{
			name: "route taken",
			prepare: func(h *harness) artist.CreateArtistCommand {
				h.store.PutRoute("routed")
				return artist.CreateArtistCommand{Name: "New", Slug: stringPointer("routed")}
			},
			code: connect.CodeAlreadyExists,
		},
		{
			name: "no longer an administrator",
			prepare: func(h *harness) artist.CreateArtistCommand {
				h.checker.Checker = (&authztest.Checker{}).Deny(create, nil)
				return artist.CreateArtistCommand{Name: "New"}
			},
			code: connect.CodePermissionDenied,
		},
		{
			name: "missing parent",
			prepare: func(*harness) artist.CreateArtistCommand {
				return artist.CreateArtistCommand{Name: "New", ParentArtistID: stringPointer("00000000-0000-4000-8000-000000000001")}
			},
			code: connect.CodeInvalidArgument, message: "parent_artist_id: artist does not exist",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			caller := h.member(newPrincipal())
			command := test.prepare(h)
			before := len(h.store.OGRequests())

			_, err := h.service.CreateArtist(caller.context(t), command)

			requireCode(t, err, test.code, test.message)
			require.Empty(t, h.audit.Records())
			require.Empty(t, h.checker.Applied())
			require.Len(t, h.store.OGRequests(), before)
		})
	}
}

func TestCreateArtistRollsBackWhenTheAuditFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	h := newHarness(t, withAudit(&audittest.Recorder{Err: auditDown}))
	caller := h.member(newPrincipal())

	_, err := h.service.CreateArtist(caller.context(t), artist.CreateArtistCommand{Name: "Rolled back"})

	require.ErrorIs(t, err, auditDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	require.Empty(t, h.checker.Applied(), "the relationship batch is written after the audit")
	page, _, listErr := h.store.ListArtists(t.Context(), artist.ListPage{Limit: 10})
	require.NoError(t, listErr)
	require.Empty(t, page)
}

func TestPublishAndUnpublishArtistStampOnceAndSignalTheirFields(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value := h.seedArtist(t, "Lifecycle")

	published, err := h.service.PublishArtist(caller.context(t), artist.PublishArtistCommand{ID: value.ID})
	require.NoError(t, err)
	require.True(t, published.Changed)
	require.Equal(t, artist.StatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)
	again, err := h.service.PublishArtist(caller.context(t), artist.PublishArtistCommand{ID: value.ID})
	require.NoError(t, err)
	require.False(t, again.Changed, "a published Artist is left unchanged")
	require.Equal(t, published.PublishedAt.UTC(), again.PublishedAt.UTC())

	unpublished, err := h.service.UnpublishArtist(caller.context(t), artist.UnpublishArtistCommand{ID: value.ID})
	require.NoError(t, err)
	require.True(t, unpublished.Changed)
	require.Equal(t, artist.StatusDraft, unpublished.Status)
	stored, _ := h.store.Artist(value.ID)
	require.Equal(t, published.PublishedAt.UTC(), stored.PublishedAt.UTC(), "unpublishing keeps published_at")

	require.Equal(t, []artist.ContentUpdate{
		{ArtistID: value.ID, Kind: artist.StateUpdate, StatePaths: []string{"state.status", "state.published_at"}},
		{ArtistID: value.ID, Kind: artist.StateUpdate, StatePaths: []string{"state.status"}},
	}, h.store.Updates())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditArtistUpdated, sharedtelemetry.AuditArtistUpdated}, auditActions(h.audit))
}

func TestPublishArtistDeniedAfterLockingTheRowAnswersNotFound(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value := h.seedArtist(t, "Locked")
	publish, err := policyv1.Artist.Publish(value.ID)
	require.NoError(t, err)
	h.checker.Checker = authztest.AllowAll().Deny(publish, nil)

	_, err = h.service.PublishArtist(caller.context(t), artist.PublishArtistCommand{ID: value.ID})

	requireCode(t, err, connect.CodeNotFound, "artist not found: "+value.ID)
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
	stored, _ := h.store.Artist(value.ID)
	require.Equal(t, artist.StatusDraft, stored.Status)
}
