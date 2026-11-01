package referencecatalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func placeOwner(id string) referencecatalog.AssetOwner {
	return referencecatalog.AssetOwner{Type: "map_place", ID: id}
}

func imageAudit(placeID string, op sharedtelemetry.AuditCollectionOperation, fileID string) sharedtelemetry.AuditRecord {
	record := audited(sharedtelemetry.AuditMapPlaceUpdated, "map_place", placeID, "image")
	record.CollectionOperation, record.FileID = op, fileID
	return record
}

// seedAuthor makes p a Member the store resolves.
func (f *fixture) seedAuthor(p principal) {
	f.store.SeedMemberWithID(p.memberID, "Author")
}

func TestMapPlaceLifecycleBindsImagesAndAuditsEachChange(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	f.seedAuthor(p)
	firstID, firstAsset := f.store.SeedReadyFile("map_image")
	secondID, secondAsset := f.store.SeedReadyFile("map_image")
	google := "  google-place  "

	created, err := f.service.CreateMapPlace(p.ctx, referencecatalog.CreateMapPlaceCommand{
		Name: "Hall", Address: "1 Hall Road", Lat: 37.5, Lng: 127, ImageFileID: &firstID, GooglePlaceID: &google,
	})
	require.NoError(t, err)
	id := created.Place.ID
	require.Equal(t, "google-place", *created.Place.GooglePlaceID)
	require.Equal(t, p.memberID, *created.Place.CreatedByMemberID)
	require.Equal(t, firstAsset, created.ImageAsset)
	require.Equal(t, &referencecatalog.MemberSummary{ID: p.memberID, Nickname: "Author"}, created.CreatedByMember)
	require.Equal(t, map[string]string{"image": firstID}, f.store.Bindings(placeOwner(id)))

	name, address := "Hall updated", "2 Hall Road"
	updated, err := f.service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: id, Name: &name, Address: &address, ImageFileID: &secondID})
	require.NoError(t, err)
	require.Equal(t, secondID, *updated.Place.ImageFileID)
	require.Equal(t, secondAsset, updated.ImageAsset)
	require.Equal(t, map[string]string{"image": secondID}, f.store.Bindings(placeOwner(id)))

	before, _ := f.store.StoredMapPlace(id)
	f.store.Fail("SaveMapPlace", errors.New("an unchanged place is not saved"))
	_, err = f.service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: id, Name: &name, Address: &address, ImageFileID: &secondID})
	require.NoError(t, err)
	after, _ := f.store.StoredMapPlace(id)
	require.Equal(t, before, after, "an identical update is a no-op")
	f.store.Fail("SaveMapPlace", nil)

	cleared, err := f.service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: id, ClearImage: true, ImageFileID: &firstID})
	require.NoError(t, err)
	require.Nil(t, cleared.Place.ImageFileID, "clearing takes precedence over a supplied image")
	require.Nil(t, cleared.ImageAsset)
	require.Empty(t, f.store.Bindings(placeOwner(id)))

	require.NoError(t, f.service.DeleteMapPlace(p.ctx, referencecatalog.DeleteMapPlaceCommand{ID: id}))
	_, stored := f.store.StoredMapPlace(id)
	require.False(t, stored)

	f.requireAudited(t, p,
		audited(sharedtelemetry.AuditMapPlaceCreated, "map_place", id),
		audited(sharedtelemetry.AuditMapPlaceUpdated, "map_place", id, "address", "name"),
		imageAudit(id, sharedtelemetry.AuditCollectionOperationAdded, secondID),
		imageAudit(id, sharedtelemetry.AuditCollectionOperationRemoved, secondID),
		audited(sharedtelemetry.AuditMapPlaceDeleted, "map_place", id),
	)
	require.Equal(t, [][]policyv1.RelationshipMutation{
		relationship(t, policyv1.MapPlace.TouchPolicy, id), relationship(t, policyv1.MapPlace.DeletePolicy, id),
	}, f.checker.Applied())
}

func TestMapPlaceUpdatePlansEachFieldAndStampsTheEditor(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	f.seedAuthor(p)
	creator := f.store.SeedMember("Creator")
	city, google := "Seoul", "google"
	place := f.store.SeedMapPlace(referencecatalog.MapPlace{
		Name: "Place", Address: "Road", Lat: 1, Lng: 2, GooglePlaceID: &google,
		AddressComponents: &referencecatalog.AddressComponents{City: &city}, CreatedByMemberID: &creator, UpdatedByMemberID: &creator,
	})
	lat, lng, empty, blank := 0.0, 0.0, "", " "
	nextCity := "Busan"

	updated, err := f.service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{
		ID: place.ID, Address: &empty, Lat: &lat, Lng: &lng, GooglePlaceID: &blank,
		AddressComponents: &referencecatalog.AddressComponents{City: &nextCity},
	})
	require.NoError(t, err)

	require.Equal(t, "Road", updated.Place.Address, "an empty address keeps the stored one")
	require.Zero(t, updated.Place.Lat, "zero coordinates are a change")
	require.Nil(t, updated.Place.GooglePlaceID, "a blank Google place ID clears it")
	require.Equal(t, "Busan", *updated.Place.AddressComponents.City)
	require.Equal(t, creator, *updated.Place.CreatedByMemberID)
	require.Equal(t, p.memberID, *updated.Place.UpdatedByMemberID)
	require.Equal(t, "Creator", updated.CreatedByMember.Nickname)
	require.Equal(t, "Author", updated.UpdatedByMember.Nickname)
	f.requireAudited(t, p, audited(sharedtelemetry.AuditMapPlaceUpdated, "map_place", place.ID, "address_components", "google_place_id", "lat", "lng"))

	for name, command := range map[string]referencecatalog.UpdateMapPlaceCommand{
		"blank name": {ID: place.ID, Name: &blank},
		"latitude":   {ID: place.ID, Lat: new(90.5)},
		"longitude":  {ID: place.ID, Lng: new(-180.5)},
		"missing id": {},
	} {
		_, err := f.service.UpdateMapPlace(p.ctx, command)
		require.Equal(t, connect.CodeInvalidArgument, codeOf(err), name)
	}
}

func TestMapPlaceCreateValidatesBeforeAuthorization(t *testing.T) {
	f, p := newFixture(t, &authztest.Checker{}, nil), newPrincipal(t)
	for name, command := range map[string]referencecatalog.CreateMapPlaceCommand{
		"name":      {Address: "Road"},
		"address":   {Name: "Place"},
		"latitude":  {Name: "Place", Address: "Road", Lat: -90.1},
		"longitude": {Name: "Place", Address: "Road", Lng: 180.1},
	} {
		_, err := f.service.CreateMapPlace(p.ctx, command)
		require.Equal(t, connect.CodeInvalidArgument, codeOf(err), name)
	}
	require.Empty(t, f.units.Steps())
	require.Empty(t, f.checker.Checks())
}

func TestMapPlaceMutationsNameTheRoleTheyNeed(t *testing.T) {
	createCan, err := policyv1.MapPlace.Create()
	require.NoError(t, err)
	f, p := newFixture(t, authztest.AllowAll().Deny(createCan, nil), nil), newPrincipal(t)
	place := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Place", Address: "Road"})
	deleteCan, err := policyv1.MapPlace.Delete(place.ID)
	require.NoError(t, err)
	f.checker.Deny(deleteCan, nil)

	_, err = f.service.CreateMapPlace(p.ctx, referencecatalog.CreateMapPlaceCommand{Name: "Denied", Address: "Road"})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	require.Equal(t, "author access required", messageOf(err))
	err = f.service.DeleteMapPlace(p.ctx, referencecatalog.DeleteMapPlaceCommand{ID: place.ID})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	require.Equal(t, "admin access required", messageOf(err))
	_, kept := f.store.StoredMapPlace(place.ID)
	require.True(t, kept)

	f.fence.Deactivate(p.identityID)
	name := "Fenced"
	_, err = f.service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: place.ID, Name: &name})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err), "an inactive author is denied")
	require.Equal(t, "author access required", messageOf(err))
	require.Equal(t, []string{p.identityID, p.identityID, p.identityID}, f.fence.Locked())

	_, err = f.service.CreateMapPlace(context.Background(), referencecatalog.CreateMapPlaceCommand{Name: "Anonymous", Address: "Road"})
	require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
	require.Equal(t, "authentication required", messageOf(err))
	require.Empty(t, f.audit.Records())
}

func TestMapPlaceAccessAnswersAnUnavailableSpiceDB(t *testing.T) {
	f, p := newFixture(t, authztest.AllowAll().Fail(errors.New("spicedb down")), nil), newPrincipal(t)

	_, err := f.service.CreateMapPlace(p.ctx, referencecatalog.CreateMapPlaceCommand{Name: "Place", Address: "Road"})

	require.Equal(t, connect.CodeUnavailable, codeOf(err))
	require.Equal(t, mutation.StepRollback, f.lastStep())
}

func TestMapPlaceDeleteRefusesAPlaceInUseAndRollsBackWhenAuditFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	f, p := newFixture(t, nil, &audittest.Recorder{Err: auditDown}), newPrincipal(t)
	used := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Used", Address: "Road"})
	f.store.SeedMapPlaceUse(used.ID)
	unused := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Unused", Address: "Road"})

	err := f.service.DeleteMapPlace(p.ctx, referencecatalog.DeleteMapPlaceCommand{ID: used.ID})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	require.Equal(t, "map place is still referenced", messageOf(err))

	require.ErrorIs(t, f.service.DeleteMapPlace(p.ctx, referencecatalog.DeleteMapPlaceCommand{ID: unused.ID}), auditDown)
	_, kept := f.store.StoredMapPlace(unused.ID)
	require.True(t, kept)
	require.Empty(t, f.checker.Applied())

	name := "Must not commit"
	_, err = f.service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: unused.ID, Name: &name})
	require.ErrorIs(t, err, auditDown)
	stored, _ := f.store.StoredMapPlace(unused.ID)
	require.Equal(t, "Unused", stored.Name)

	_, err = f.service.CreateMapPlace(p.ctx, referencecatalog.CreateMapPlaceCommand{Name: "Rolled back", Address: "Road"})
	require.ErrorIs(t, err, auditDown)
	page, err := f.service.ListMapPlacesAdmin(p.ctx, referencecatalog.ListMapPlacesAdminQuery{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
}

func TestMapPlaceMutationsOfAMissingPlaceAnswerNotFound(t *testing.T) {
	f, p := newFixture(t, &authztest.Checker{}, nil), newPrincipal(t)
	missing := uuid.NewString()
	name := "Missing"

	_, err := f.service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: missing, Name: &name})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	require.Equal(t, "map place not found: "+missing, messageOf(err))
	require.Equal(t, connect.CodeNotFound, codeOf(f.service.DeleteMapPlace(p.ctx, referencecatalog.DeleteMapPlaceCommand{ID: missing})))
	require.Equal(t, connect.CodeInvalidArgument, codeOf(f.service.DeleteMapPlace(p.ctx, referencecatalog.DeleteMapPlaceCommand{})))
	require.Empty(t, f.checker.Checks())
}

func TestMapPlaceReadsFailClosedOnAnUnresolvedMember(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	unresolved := uuid.NewString()
	place := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Place", Address: "Road", CreatedByMemberID: &unresolved})
	blankNickname := f.store.SeedMember(" ")
	blank := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Blank", Address: "Road", UpdatedByMemberID: &blankNickname})

	_, err := f.service.GetMapPlace(p.ctx, referencecatalog.GetMapPlaceQuery{ID: place.ID})
	require.ErrorContains(t, err, "map place references unresolved member "+unresolved)
	_, err = f.service.GetMapPlacesByIDs(p.ctx, referencecatalog.GetMapPlacesByIDsQuery{IDs: []string{blank.ID}})
	require.ErrorContains(t, err, "map place references unresolved member "+blankNickname)
	_, err = f.service.ListMapPlacesAdmin(p.ctx, referencecatalog.ListMapPlacesAdminQuery{})
	require.Error(t, err)
}

func TestMapPlaceReadsKeepTheRequestedOrderAndResolveImages(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	fileID, asset := f.store.SeedReadyFile("map_image")
	unready := uuid.NewString()
	first := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "First", Address: "Road", ImageFileID: &fileID})
	second := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Second", Address: "Road"})

	places, err := f.service.GetMapPlacesByIDs(p.ctx, referencecatalog.GetMapPlacesByIDsQuery{IDs: []string{second.ID, uuid.NewString(), first.ID}})
	require.NoError(t, err)
	require.Equal(t, []referencecatalog.MapPlaceView{{Place: second}, {Place: first, ImageAsset: asset}}, places)
	empty, err := f.service.GetMapPlacesByIDs(p.ctx, referencecatalog.GetMapPlacesByIDsQuery{})
	require.NoError(t, err)
	require.Empty(t, empty)

	view, err := f.service.GetMapPlace(p.ctx, referencecatalog.GetMapPlaceQuery{ID: first.ID})
	require.NoError(t, err)
	require.Equal(t, asset, view.ImageAsset)
	_, err = f.service.GetMapPlace(p.ctx, referencecatalog.GetMapPlaceQuery{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	_, err = f.service.GetMapPlace(p.ctx, referencecatalog.GetMapPlaceQuery{})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))

	broken := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Broken", Address: "Road", ImageFileID: &unready})
	_, err = f.service.GetMapPlace(p.ctx, referencecatalog.GetMapPlaceQuery{ID: broken.ID})
	require.Equal(t, connect.CodeNotFound, codeOf(err), "an image without a ready asset fails the read")
}

func TestMapPlaceSearchTrimsTheQueryAndBoundsTheLimit(t *testing.T) {
	f := newFixture(t, nil, nil)
	for range 55 {
		f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Hall " + uuid.NewString(), Address: "Road"})
	}
	f.store.Fail("SearchMapPlaces", errors.New("a blank query reads nothing"))
	blank, err := f.service.SearchMapPlaces(context.Background(), referencecatalog.SearchMapPlacesQuery{Query: "   "})
	require.NoError(t, err)
	require.Empty(t, blank)
	require.NotNil(t, blank)
	f.store.Fail("SearchMapPlaces", nil)
	for limit, want := range map[int32]int{0: 10, 7: 7, 99: 50} {
		found, err := f.service.SearchMapPlaces(context.Background(), referencecatalog.SearchMapPlacesQuery{Query: " hall ", Limit: limit})
		require.NoError(t, err)
		require.Len(t, found, want, limit)
	}
}

func TestListMapPlacesAdminRequiresAnAdministratorBeforeTheStore(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal *auth.UserInfo
		code      connect.Code
	}{
		{"anonymous", nil, connect.CodeUnauthenticated},
		{"unauthenticated", &auth.UserInfo{MemberID: "11111111-1111-4111-8111-111111111111", IdentityID: "22222222-2222-4222-8222-222222222222"}, connect.CodeUnauthenticated},
		{"unlinked", &auth.UserInfo{Authenticated: true, IdentityID: "22222222-2222-4222-8222-222222222222"}, connect.CodeUnauthenticated},
		{"banned", &auth.UserInfo{Authenticated: true, MemberID: "11111111-1111-4111-8111-111111111111", IdentityID: "22222222-2222-4222-8222-222222222222", Banned: true}, connect.CodePermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t, nil, nil)
			f.store.Fail("ListMapPlaces", errors.New("the store is not reached"))
			ctx := context.Background()
			if test.principal != nil {
				ctx = auth.WithUser(ctx, test.principal)
			}
			_, err := f.service.ListMapPlacesAdmin(ctx, referencecatalog.ListMapPlacesAdminQuery{})
			require.Equal(t, test.code, codeOf(err))
		})
	}

	f, p := newFixture(t, &authztest.Checker{}, nil), newPrincipal(t)
	_, err := f.service.ListMapPlacesAdmin(p.ctx, referencecatalog.ListMapPlacesAdminQuery{})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	require.Equal(t, "admin access required", messageOf(err))
}

func TestListMapPlacesAdminPagesNewestFirst(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	older := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Older", Address: "Road", CreatedAt: at, UpdatedAt: at})
	newer := f.store.SeedMapPlace(referencecatalog.MapPlace{Name: "Newer", Address: "Road", CreatedAt: at, UpdatedAt: at.Add(time.Second)})

	page, err := f.service.ListMapPlacesAdmin(p.ctx, referencecatalog.ListMapPlacesAdminQuery{})
	require.NoError(t, err)
	require.Equal(t, []referencecatalog.MapPlaceView{{Place: newer}, {Place: older}}, page.Items)
	require.Equal(t, referencecatalog.Pagination{Total: 2, Limit: 20}, page.Pagination)
}
