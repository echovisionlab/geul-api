package artist_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/artist/artisttest"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPreviewDeleteArtistListsEveryRelationInDomainOrder(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	parent := h.seedArtist(t, "Parent")
	child := h.seedArtist(t, "Child", func(value *artist.Artist) { value.ParentArtistID = &parent.ID })
	labelID := uuid.NewString()
	h.store.PutLabel(labelID, "Label", true)
	h.store.LinkLabels(parent.ID, labelID)
	h.store.PutRelations(parent.ID, artisttest.Relations{
		Works:    []entityparticipant.Related{{ID: "work", DisplayName: "Work", RelationCount: 2}},
		Releases: []entityparticipant.Related{{ID: "release", DisplayName: "Release", RelationCount: 1}},
	})

	preview, err := h.service.PreviewDeleteArtist(caller.context(t), artist.PreviewDeleteArtistQuery{ID: parent.ID})

	require.NoError(t, err)
	require.NotEmpty(t, preview.Revision)
	require.Equal(t, []artist.DeletionImpact{
		{Domain: artist.RelationChildArtist, EntityID: child.ID, Label: "Child", RelationCount: 1},
		{Domain: artist.RelationLabel, EntityID: labelID, Label: "Label", RelationCount: 1},
		{Domain: artist.RelationRelease, EntityID: "release", Label: "Release", RelationCount: 1},
		{Domain: artist.RelationWork, EntityID: "work", Label: "Work", RelationCount: 2},
	}, preview.Impacts)
}

func TestDeleteArtistChecksThePreviewRevisionThenRemovesEverything(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	parent := h.seedArtist(t, "Parent")
	child := h.seedArtist(t, "Child", func(value *artist.Artist) { value.ParentArtistID = &parent.ID })
	documentID := h.documentID(t, parent.ID)
	preview, err := h.service.PreviewDeleteArtist(caller.context(t), artist.PreviewDeleteArtistQuery{ID: parent.ID})
	require.NoError(t, err)
	policy, err := policyv1.Artist.TouchPolicy(parent.ID)
	require.NoError(t, err)
	childParent, err := policyv1.Artist.TouchParent(child.ID, parent.ID)
	require.NoError(t, err)
	h.checker.Snapshot = []policyv1.RelationshipMutation{policy, childParent}

	h.seedArtist(t, "Second child", func(value *artist.Artist) { value.ParentArtistID = &parent.ID })
	err = h.service.DeleteArtist(caller.context(t), artist.DeleteArtistCommand{ID: parent.ID, ExpectedRevision: preview.Revision})
	requireCode(t, err, connect.CodeFailedPrecondition, "Artist relations changed; preview deletion again")
	_, exists := h.store.Artist(parent.ID)
	require.True(t, exists)
	require.Empty(t, h.checker.Applied())

	preview, err = h.service.PreviewDeleteArtist(caller.context(t), artist.PreviewDeleteArtistQuery{ID: parent.ID})
	require.NoError(t, err)
	locks := h.store.ParentGraphLocks()
	require.NoError(t, h.service.DeleteArtist(caller.context(t), artist.DeleteArtistCommand{ID: parent.ID, ExpectedRevision: preview.Revision}))

	_, exists = h.store.Artist(parent.ID)
	require.False(t, exists)
	_, exists = h.store.Document(documentID)
	require.False(t, exists)
	stored, _ := h.store.Artist(child.ID)
	require.Nil(t, stored.ParentArtistID, "a deleted parent leaves its children at the root")
	require.Equal(t, []string{parent.ID}, h.store.DeletedShareLinks())
	require.Equal(t, []string{parent.ID}, h.store.OGCancels())
	require.Equal(t, []string{parent.ID}, h.store.ReleasedImages())
	require.Equal(t, locks+1, h.store.ParentGraphLocks(), "the delete serializes with reparents")
	require.Len(t, h.checker.Applied(), 1)
	require.Len(t, h.checker.Applied()[0], 2, "the snapshot relationships are deleted")
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditArtistDeleted}, auditActions(h.audit))
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess}, h.units.Steps()[len(h.units.Steps())-6:len(h.units.Steps())-4])
}

func TestDeleteArtistRequiresAnExpectedRevision(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())

	err := h.service.DeleteArtist(caller.context(t), artist.DeleteArtistCommand{ID: uuid.NewString()})

	requireCode(t, err, connect.CodeInvalidArgument, "expected_revision is required")
}

func TestSetArtistImagesReplacesTheGalleryUnderItsRevision(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value := h.seedArtist(t, "Gallery")
	h.store.OGRunID = "run"
	first, second := uuid.NewString(), uuid.NewString()
	h.store.PutReadyImage(first, &commonv1.AssetRef{AssetId: "first-asset"})
	editor, err := h.service.GetArtistEditorData(caller.context(t), artist.GetArtistEditorDataQuery{ID: value.ID})
	require.NoError(t, err)

	update, err := h.service.SetArtistImages(caller.context(t), artist.SetArtistImagesCommand{
		ArtistID: value.ID, FileIDs: []string{first, second}, ExpectedRevision: editor.ImageRevision,
	})

	require.NoError(t, err)
	require.Equal(t, []string{first, second}, h.store.ImageFiles(value.ID))
	require.Len(t, update.Images, 2)
	require.Equal(t, "first-asset", update.Images[0].Asset.GetAssetId())
	require.Equal(t, "run", *update.OgGenerationRunID)
	require.Equal(t, []artisttest.OGRequest{{ArtistID: value.ID, Reason: "artist_primary_image_updated"}}, h.store.OGRequests())
	require.Equal(t, []artist.ContentUpdate{{ArtistID: value.ID, Kind: artist.MediaUpdate}}, h.store.Updates())

	_, err = h.service.SetArtistImages(caller.context(t), artist.SetArtistImagesCommand{
		ArtistID: value.ID, FileIDs: []string{second}, ExpectedRevision: editor.ImageRevision,
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "Artist images changed; reload before saving")

	reordered, err := h.service.SetArtistImage(caller.context(t), artist.SetArtistImageCommand{ArtistID: value.ID, FileID: first})
	require.NoError(t, err)
	require.Nil(t, reordered.OgGenerationRunID, "an unchanged primary image keeps the OG image")
	require.Len(t, h.store.OGRequests(), 1)

	cleared, err := h.service.DeleteArtistImage(caller.context(t), artist.DeleteArtistImageCommand{ArtistID: value.ID})
	require.NoError(t, err)
	require.Empty(t, cleared.Images)
	require.Empty(t, h.store.ImageFiles(value.ID))
	require.Len(t, auditActions(h.audit), 3)
}

func TestSetArtistImagesRejectsDuplicateAndMissingInput(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value := h.seedArtist(t, "Gallery")
	file := uuid.NewString()

	_, err := h.service.SetArtistImages(caller.context(t), artist.SetArtistImagesCommand{ArtistID: value.ID, FileIDs: []string{file, file}, ExpectedRevision: "revision"})
	requireCode(t, err, connect.CodeInvalidArgument, "file_ids: must not contain duplicates")
	_, err = h.service.SetArtistImages(caller.context(t), artist.SetArtistImagesCommand{ArtistID: value.ID, FileIDs: []string{file}})
	requireCode(t, err, connect.CodeInvalidArgument, "expected_revision is required")
	_, err = h.service.SetArtistImage(caller.context(t), artist.SetArtistImageCommand{ArtistID: uuid.NewString(), FileID: file})
	requireCode(t, err, connect.CodeNotFound, "")
	require.Empty(t, h.store.ImageFiles(value.ID))
}

func TestSetArtistParticipantWiresTheArtistSpec(t *testing.T) {
	h := newHarness(t)
	value := h.seedArtist(t, "Participants")
	owner := h.member(newPrincipal())
	h.store.PutParticipant(value.ID, owner.memberID, entityparticipant.RoleOwner, time.Now().Add(-time.Hour))
	manager := h.member(newPrincipal())

	view, err := h.service.SetArtistParticipant(owner.context(t), artist.SetArtistParticipantCommand{
		ArtistID: value.ID, MemberID: manager.memberID, Role: entityparticipant.RoleManager,
	})

	require.NoError(t, err)
	require.Equal(t, manager.memberID, view.Member.GetId())
	require.Equal(t, entityparticipant.RoleManager, h.store.Participants(value.ID)[manager.memberID])
	actor, err := policyv1.NewAccountIdentityActor(manager.identityID)
	require.NoError(t, err)
	touch, err := policyv1.Artist.TouchManager(value.ID, actor)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditArtistUpdated}, auditActions(h.audit))

	listed, err := h.service.ListArtistParticipants(owner.context(t), artist.ListArtistParticipantsQuery{ArtistID: value.ID})
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, owner.memberID, listed[0].Member.GetId())

	err = h.service.RemoveArtistParticipant(owner.context(t), artist.RemoveArtistParticipantCommand{ArtistID: value.ID, MemberID: owner.memberID})
	requireCode(t, err, connect.CodeFailedPrecondition, "")
}

func TestListMyArtistsListsTheArtistsTheCallerMayManage(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	managed := h.seedArtist(t, "Managed")
	h.seedArtist(t, "Other")

	empty, err := h.service.ListMyArtists(caller.context(t), artist.ListArtistsQuery{})
	require.NoError(t, err)
	require.Equal(t, artist.ArtistPage{Artists: []artist.View{}, Pagination: artist.Pagination{Limit: 20}}, empty)

	h.checker.Resources = []string{managed.ID}
	page, err := h.service.ListMyArtists(caller.context(t), artist.ListArtistsQuery{})
	require.NoError(t, err)
	require.Len(t, page.Artists, 1)
	require.Equal(t, managed.ID, page.Artists[0].Artist.ID)
	require.Equal(t, "Managed", page.Artists[0].Name)
	require.Equal(t, artist.Pagination{Total: 1, Limit: 20}, page.Pagination)
}

func TestListArtistsAdminCountsReleasesAndKeepsDrafts(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	draft := h.seedArtist(t, "Draft")
	published := h.seedArtist(t, "Published", func(value *artist.Artist) { value.Status = artist.StatusPublished })
	h.store.PutReleaseCount(published.ID, 3)

	admin, err := h.service.ListArtistsAdmin(caller.context(t), artist.ListArtistsQuery{})
	require.NoError(t, err)
	require.EqualValues(t, 2, admin.Pagination.Total)
	counts := map[string]int32{}
	for _, item := range admin.Artists {
		counts[item.Artist.Artist.ID] = item.ReleaseCount
	}
	require.Equal(t, map[string]int32{draft.ID: 0, published.ID: 3}, counts)

	public, err := h.service.ListArtists(caller.context(t), artist.ListArtistsQuery{})
	require.NoError(t, err)
	require.Len(t, public.Artists, 1)
	require.Equal(t, published.ID, public.Artists[0].Artist.ID)
}
