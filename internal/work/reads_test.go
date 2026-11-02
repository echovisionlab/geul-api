package work_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/stretchr/testify/require"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestGetWorkProjectsTheSourceDocumentAssetsAndClients(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Projected", func(value *work.Work) { value.FeaturedImageFileID = stringPointer("image-file") })
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "en", Title: stringPointer("Projected"), Summary: stringPointer("Summary")})
	h.store.PutLocaleOgAsset(value.ID, "en", "og-asset")
	h.store.PutReadyAsset("og-asset", &mediaasset.Reference{URL: "og"})
	h.store.PutFileAsset("image-file", "image", &mediaasset.Reference{URL: "featured"})
	h.store.PutClient(work.ClientRow{ID: "client-1", Name: "Client", LightFileID: stringPointer("logo-file")})
	h.store.PutFileAsset("logo-file", "logo", &mediaasset.Reference{URL: "logo"})
	h.store.LinkClients(value.ID, "client-1")

	view, err := h.service.GetWork(admin.context(t), work.GetWorkQuery{ID: value.ID})

	require.NoError(t, err)
	require.Equal(t, "Projected", view.Title)
	require.Equal(t, "Summary", *view.Summary)
	require.Equal(t, "og", optional.Value(view.OgAsset).URL)
	require.Equal(t, "featured", optional.Value(view.FeaturedImage).URL)
	require.Equal(t, h.revision(t, value.ID).String(), view.Revision)
	require.NotNil(t, view.Document)
	require.Equal(t, []work.Client{{ID: "client-1", Name: "Client", LightLogo: &mediaasset.Reference{URL: "logo"}}}, view.Clients)
}

func TestGetWorkHidesAWorkTheCallerMayNotView(t *testing.T) {
	h := newHarness(t)
	viewer := newPrincipal()
	draft := h.seedWork(t, "Draft")
	archived := h.seedWork(t, "Archived", func(value *work.Work) { value.Status = work.StatusArchived })
	view, err := policyv1.Work.View(draft.ID)
	require.NoError(t, err)
	viewArchived, err := policyv1.Work.ViewArchived(archived.ID)
	require.NoError(t, err)
	h.checker.Deny(view, nil).Deny(viewArchived, nil)

	_, err = h.service.GetWork(viewer.context(t), work.GetWorkQuery{ID: draft.ID})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+draft.ID)
	_, err = h.service.GetWork(viewer.context(t), work.GetWorkQuery{ID: archived.ID})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+archived.ID)

	value := h.seedWork(t, "Fenced")
	h.fence.Deactivate(viewer.identityID)
	_, err = h.service.GetWork(viewer.context(t), work.GetWorkQuery{ID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+value.ID)
}

func TestListWorksAdminCountsEachWorksCreditsAndClients(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	first := h.seedWork(t, "First")
	second := h.seedWork(t, "Second")
	h.store.PutCredit(work.Credit{WorkID: first.ID, Name: stringPointer("Credit")})
	h.store.PutCredit(work.Credit{WorkID: first.ID, Name: stringPointer("Credit")})
	h.store.PutClient(work.ClientRow{ID: "client-1", Name: "Client"})
	h.store.LinkClients(second.ID, "client-1")

	page, err := h.service.ListWorksAdmin(admin.context(t), work.ListWorksAdminQuery{})

	require.NoError(t, err)
	require.Equal(t, work.Pagination{Total: 2, Limit: 20}, page.Pagination)
	counts := map[string][2]int32{}
	for _, listed := range page.Works {
		counts[listed.View.ID] = [2]int32{listed.CreditCount, listed.ClientCount}
		require.NotEmpty(t, listed.View.Revision)
	}
	require.Equal(t, map[string][2]int32{first.ID: {2, 0}, second.ID: {0, 1}}, counts)

	h.store.Fail = map[string]error{"CountCredits": errStoreDown}
	page, err = h.service.ListWorksAdmin(admin.context(t), work.ListWorksAdminQuery{})
	require.NoError(t, err, "a failed count lists zero")
	require.Zero(t, page.Works[0].CreditCount)

	list, err := policyv1.Work.List()
	require.NoError(t, err)
	h.checker.Deny(list, nil)
	_, err = h.service.ListWorksAdmin(admin.context(t), work.ListWorksAdminQuery{})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
}

func TestCheckWorkSlugAvailableConsultsWorksAndRoutes(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	holder := h.seedWork(t, "Holder", func(value *work.Work) { value.Slug = stringPointer("held") })
	h.store.PutRoute("routed")
	ctx := admin.context(t)

	for slug, want := range map[string]bool{"held": false, "routed": false, "free": true} {
		available, err := h.service.CheckWorkSlugAvailable(ctx, work.CheckWorkSlugAvailableQuery{Slug: slug})
		require.NoError(t, err)
		require.Equal(t, want, available, slug)
	}
	available, err := h.service.CheckWorkSlugAvailable(ctx, work.CheckWorkSlugAvailableQuery{Slug: "held", ExcludeWorkID: &holder.ID})
	require.NoError(t, err)
	require.True(t, available, "a Work's own slug is free for it")

	manage, err := policyv1.Work.Manage(holder.ID)
	require.NoError(t, err)
	h.checker.Deny(manage, nil)
	_, err = h.service.CheckWorkSlugAvailable(ctx, work.CheckWorkSlugAvailableQuery{Slug: "held", ExcludeWorkID: &holder.ID})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+holder.ID)
	_, err = h.service.CheckWorkSlugAvailable(ctx, work.CheckWorkSlugAvailableQuery{Slug: "two/segments"})
	requireCode(t, err, connect.CodeInvalidArgument, "slug: must not contain '/'")
}

func TestWorkFeaturedImageBindsOnceAndReleasesTheCurrentImage(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Pictured")
	h.store.PutFile("file-1")
	h.store.PutFileAsset("file-1", "image", &mediaasset.Reference{URL: "image"})
	h.store.OGRunID = "og-run"

	set, err := h.service.SetWorkFeaturedImage(admin.context(t), work.SetWorkFeaturedImageCommand{WorkID: value.ID, FileID: "file-1"})
	require.NoError(t, err)
	require.Equal(t, "image", optional.Value(set.ImageAsset).URL)
	require.Equal(t, "og-run", *set.OgGenerationRunID)
	bound, ok := h.store.FeaturedImageBinding(value.ID)
	require.True(t, ok)
	require.Equal(t, "file-1", bound)
	require.Equal(t, []work.ContentUpdate{{WorkID: value.ID, Kind: work.MediaUpdate}}, h.store.Updates())

	again, err := h.service.SetWorkFeaturedImage(admin.context(t), work.SetWorkFeaturedImageCommand{WorkID: value.ID, FileID: "file-1"})
	require.NoError(t, err)
	require.Nil(t, again.OgGenerationRunID, "setting the current image changes nothing")
	require.Len(t, h.audit.Records(), 1)

	_, err = h.service.SetWorkFeaturedImage(admin.context(t), work.SetWorkFeaturedImageCommand{WorkID: value.ID, FileID: "missing"})
	requireCode(t, err, connect.CodeFailedPrecondition, "one or more files no longer exist")

	runID, err := h.service.DeleteWorkFeaturedImage(admin.context(t), work.DeleteWorkFeaturedImageCommand{WorkID: value.ID})
	require.NoError(t, err)
	require.Equal(t, "og-run", *runID)
	require.Nil(t, h.stored(t, value.ID).FeaturedImageFileID)
	_, ok = h.store.FeaturedImageBinding(value.ID)
	require.False(t, ok)
	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, records[1].CollectionOperation)
	require.Equal(t, "file-1", records[1].AssetID)
}

func TestDeleteWorkFeaturedImageReadsTheImageAfterTheRootLock(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Pictured")
	h.store.Before = map[string]func(){"LockRoot": func() {
		h.store.Before = nil
		stored := h.stored(t, value.ID)
		stored.FeaturedImageFileID = stringPointer("concurrent-file")
		h.store.PutWork(stored)
	}}

	_, err := h.service.DeleteWorkFeaturedImage(admin.context(t), work.DeleteWorkFeaturedImageCommand{WorkID: value.ID})

	require.NoError(t, err)
	require.Nil(t, h.stored(t, value.ID).FeaturedImageFileID, "the image a concurrent writer set before the lock is the one removed")
	require.Equal(t, "concurrent-file", h.audit.Records()[0].AssetID)
}

func TestRequireLockedViewAndEditAuthorizeTheLockedLifecycle(t *testing.T) {
	h := newHarness(t)
	viewer := newPrincipal()
	value := h.seedWork(t, "Policy", func(value *work.Work) { value.Status = work.StatusArchived })
	joined := work.Joined{Store: h.store}
	viewArchived, err := policyv1.Work.ViewArchived(value.ID)
	require.NoError(t, err)
	editArchived, err := policyv1.Work.EditArchived(value.ID)
	require.NoError(t, err)

	require.NoError(t, work.RequireLockedView(viewer.context(t), joined, h.fence, h.checker, value.ID))
	require.NoError(t, work.RequireLockedEdit(viewer.context(t), joined, h.fence, h.checker, value.ID))
	require.Equal(t, []authztest.Check{authztest.Of(viewer.identityID, viewArchived, nil), authztest.Of(viewer.identityID, editArchived, nil)}, h.checker.Checks())

	h.checker.Deny(editArchived, nil)
	err = work.RequireLockedEdit(viewer.context(t), joined, h.fence, h.checker, value.ID)
	requireCode(t, err, connect.CodeNotFound, "work not found: "+value.ID)
	h.fence.Deactivate(viewer.identityID)
	err = work.RequireLockedView(viewer.context(t), joined, h.fence, h.checker, value.ID)
	requireCode(t, err, connect.CodeNotFound, "work not found: "+value.ID)
	err = work.RequireLockedView(t.Context(), joined, h.fence, h.checker, value.ID)
	requireCode(t, err, connect.CodeUnauthenticated, "")
}
