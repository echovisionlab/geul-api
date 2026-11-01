package page_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreatePageStoresADraftWithItsPolicyLocaleAndOGImage(t *testing.T) {
	h := newHarness(t)
	admin := h.member(newPrincipal())
	h.store.PutPreferredLocale(admin.memberID, "ko")
	tag := uuid.NewString()
	h.store.PutUserTag(tag)

	created, err := h.service.CreatePage(admin.context(t), page.CreatePageCommand{
		Title: "  About  ", Slug: text(" about/team "), Summary: text("Who we are"),
		AccessPolicy: &pageaccess.Policy{Mode: pageaccess.ModeConditions, UserTagIDs: []string{tag}},
	})

	require.NoError(t, err)
	require.Equal(t, "About", created.Title)
	require.Equal(t, "Who we are", *created.Summary)
	require.Equal(t, "about/team", *created.Page.Slug)
	require.Equal(t, "ko", created.Page.SourceLocale, "the creator's preferred locale")
	require.Equal(t, page.StatusDraft, created.Page.Status)
	require.True(t, created.Page.ShowTitle)
	policy, err := pageaccess.Decode(created.Page.AccessPolicy)
	require.NoError(t, err)
	require.Equal(t, []string{tag}, policy.UserTagIDs)
	require.NotEmpty(t, created.Revision)
	require.Equal(t, []pagetest.OGRequest{{PageID: created.Page.ID, Locale: "ko", Reason: "page_created"}}, h.store.OGRequests())
	touch, err := policyv1.Page.TouchPolicy(created.Page.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditPageCreated}, auditActions(h.audit))
}

func TestCreatePageRejectsInvalidInputBeforeStoringAnything(t *testing.T) {
	for _, test := range []struct {
		name    string
		command page.CreatePageCommand
		seed    func(h *harness)
		code    connect.Code
		message string
	}{
		{name: "blank title", command: page.CreatePageCommand{Title: " "}, code: connect.CodeInvalidArgument, message: "title is required"},
		{name: "unsupported audience", command: page.CreatePageCommand{Title: "T", AccessPolicy: &pageaccess.Policy{Mode: 9}},
			code: connect.CodeInvalidArgument, message: "access_policy: unsupported Page access mode"},
		{name: "unknown user tag", command: page.CreatePageCommand{Title: "T", AccessPolicy: &pageaccess.Policy{Mode: pageaccess.ModeConditions, UserTagIDs: []string{uuid.NewString()}}},
			code: connect.CodeInvalidArgument, message: "access_policy.user_tag_ids: selected user tag does not exist"},
		{name: "slug another Page holds", command: page.CreatePageCommand{Title: "T", Slug: text("taken")},
			seed: func(h *harness) { h.seedPage("Holder", func(value *page.Page) { value.Slug = text("taken") }) },
			code: connect.CodeAlreadyExists, message: "page with slug 'taken' already exists"},
		{name: "slug a resource route holds", command: page.CreatePageCommand{Title: "T", Slug: text("posts/article")},
			seed: func(h *harness) { h.store.PutRoute("posts/article") }, code: connect.CodeAlreadyExists},
		{name: "unsupported source locale", command: page.CreatePageCommand{Title: "T", SourceLocale: "en-US"},
			code: connect.CodeInvalidArgument, message: "source_locale: must be a supported canonical locale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			if test.seed != nil {
				test.seed(h)
			}
			before, _, err := h.store.ListPages(t.Context(), page.ListPage{Limit: 100})
			require.NoError(t, err)

			_, err = h.service.CreatePage(h.member(newPrincipal()).context(t), test.command)

			requireCode(t, err, test.code, test.message)
			after, _, err := h.store.ListPages(t.Context(), page.ListPage{Limit: 100})
			require.NoError(t, err)
			require.Len(t, after, len(before))
			require.Empty(t, h.checker.Applied())
			require.Empty(t, h.audit.Records())
		})
	}
}

func TestCreatePageAnswersASlugRaceAsADuplicate(t *testing.T) {
	h := newHarness(t)
	h.store.Fail = map[string]error{"InsertPage": page.ErrSlugTaken}

	_, err := h.service.CreatePage(h.member(newPrincipal()).context(t), page.CreatePageCommand{Title: "T", Slug: text("raced")})

	requireCode(t, err, connect.CodeAlreadyExists, "page with slug 'slug' already exists")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
}

func TestCreatePageRequiresTheFreshCreateAction(t *testing.T) {
	create, err := policyv1.Page.Create()
	require.NoError(t, err)
	t.Run("denied", func(t *testing.T) {
		h := newHarness(t)
		h.checker.Deny(create, nil)
		_, err := h.service.CreatePage(h.member(newPrincipal()).context(t), page.CreatePageCommand{Title: "T"})
		requireCode(t, err, connect.CodePermissionDenied, "admin access required")
	})
	t.Run("inactive principal", func(t *testing.T) {
		h := newHarness(t)
		admin := h.member(newPrincipal())
		h.fence.Deactivate(admin.identityID)
		_, err := h.service.CreatePage(admin.context(t), page.CreatePageCommand{Title: "T"})
		requireCode(t, err, connect.CodePermissionDenied, "admin access required")
	})
}

func TestGetPageMasksAPageTheCallerMayNotView(t *testing.T) {
	h := newHarness(t)
	viewer := h.member(newPrincipal())
	stored := h.seedPage("Hidden", func(value *page.Page) { value.FeaturedImageFileID = text(uuid.NewString()) })

	detail, err := h.service.GetPage(viewer.context(t), page.GetPageQuery{ID: stored.ID})
	require.NoError(t, err)
	require.Equal(t, "Hidden", detail.Title)
	require.Equal(t, *stored.FeaturedImageFileID, detail.FeaturedImage.FileId)

	view, err := policyv1.Page.View(stored.ID)
	require.NoError(t, err)
	h.checker.Deny(view, nil)
	_, err = h.service.GetPage(viewer.context(t), page.GetPageQuery{ID: stored.ID})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
	_, err = h.service.GetPageBySlug(viewer.context(t), page.GetPageBySlugQuery{Slug: "missing"})
	requireCode(t, err, connect.CodeNotFound, "page not found: missing")
}

func TestGetPageMasksAnInactivePrincipalAndKeepsTheAudience(t *testing.T) {
	h := newHarness(t)
	viewer := h.member(newPrincipal())
	restricted, err := pageaccess.Encode(pageaccess.Policy{Mode: pageaccess.ModeConditions, NewsletterSubscriber: true})
	require.NoError(t, err)
	stored := h.seedPage("Members only", func(value *page.Page) { value.AccessPolicy = restricted })
	manage, err := policyv1.Page.Manage(stored.ID)
	require.NoError(t, err)
	h.checker.Deny(manage, nil)

	_, err = h.service.GetPage(viewer.context(t), page.GetPageQuery{ID: stored.ID})
	requireCode(t, err, connect.CodePermissionDenied, "you do not have permission to access this page")
	h.store.PutSubscriber(viewer.identityID)
	_, err = h.service.GetPage(viewer.context(t), page.GetPageQuery{ID: stored.ID})
	require.NoError(t, err)

	h.fence.Deactivate(viewer.identityID)
	_, err = h.service.GetPage(viewer.context(t), page.GetPageQuery{ID: stored.ID})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
}

func TestListPagesAdminPagesSourceTitlesForAnAdministrator(t *testing.T) {
	h := newHarness(t)
	admin := h.member(newPrincipal())
	base := time.Now().UTC().Add(-time.Hour)
	for index, title := range []string{"First", "Second", "Third"} {
		h.seedPage(title, func(value *page.Page) { value.CreatedAt = base.Add(time.Duration(index) * time.Minute) })
	}

	list, err := h.service.ListPagesAdmin(admin.context(t), page.ListPagesAdminQuery{Page: page.Paging{Limit: 2}})
	require.NoError(t, err)
	require.Equal(t, page.Pagination{Total: 3, Limit: 2, Offset: 0, HasMore: true}, list.Pagination)
	require.Equal(t, []string{"Third", "Second"}, []string{list.Pages[0].Title, list.Pages[1].Title})

	// An offset near the int32 limit pages past the end instead of wrapping.
	last, err := h.service.ListPagesAdmin(admin.context(t), page.ListPagesAdminQuery{Page: page.Paging{Limit: 20, Offset: math.MaxInt32 - 1}})
	require.NoError(t, err)
	require.Empty(t, last.Pages)
	require.False(t, last.Pagination.HasMore)

	listAction, err := policyv1.Page.List()
	require.NoError(t, err)
	h.checker.Deny(listAction, nil)
	_, err = h.service.ListPagesAdmin(admin.context(t), page.ListPagesAdminQuery{})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
}

func TestUpdatePageReportsAnUnchangedPageWithoutAuditOrSignal(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Page", func(value *page.Page) { value.Slug, value.ShowTitle = text("same"), true })
	public, err := pageaccess.Encode(pageaccess.Policy{})
	require.NoError(t, err)
	h.store.PutPage(func() page.Page { value, _ := h.store.Page(stored.ID); value.AccessPolicy = public; return value }(), "Page")
	shown := true

	result, err := h.service.UpdatePage(h.member(newPrincipal()).context(t), page.UpdatePageCommand{
		ID: stored.ID, Slug: text(" same "), ShowTitle: &shown, AccessPolicy: &pageaccess.Policy{},
	})

	require.NoError(t, err)
	require.False(t, result.Changed)
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.Updates())
	require.Empty(t, h.store.MenuUpdates())
}

func TestUpdatePageRewritesMenuTargetsOfAChangedSlug(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Page", func(value *page.Page) { value.Slug, value.ShowTitle = text("old"), true })
	hidden := false

	result, err := h.service.UpdatePage(h.member(newPrincipal()).context(t), page.UpdatePageCommand{
		ID: stored.ID, Slug: text("new"), ShowTitle: &hidden,
		AccessPolicy: &pageaccess.Policy{Mode: pageaccess.ModeAuthenticated},
	})

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, "new", *result.Page.Slug)
	updated, _ := h.store.Page(stored.ID)
	require.Equal(t, "new", *updated.Slug)
	require.False(t, updated.ShowTitle)
	require.True(t, result.Page.UpdatedAt.Equal(updated.UpdatedAt))
	require.Equal(t, []pagetest.MenuUpdate{{PageID: stored.ID, PreviousSlug: "old", Next: "new"}}, h.store.MenuUpdates())
	require.Equal(t, []page.ContentUpdate{{PageID: stored.ID, Kind: page.SettingsUpdate, Fields: []string{"slug", "showTitle", "accessPolicy"}}}, h.store.Updates())
	require.Len(t, h.audit.Records(), 1)
	require.ElementsMatch(t, []string{"slug", "show_title", "access_policy"}, h.audit.Records()[0].ChangedFields)

	// Clearing the slug points the Menu links at the Page's ID.
	_, err = h.service.UpdatePage(h.member(newPrincipal()).context(t), page.UpdatePageCommand{ID: stored.ID, Slug: text(" ")})
	require.NoError(t, err)
	require.Equal(t, pagetest.MenuUpdate{PageID: stored.ID, PreviousSlug: "new", Next: stored.ID}, h.store.MenuUpdates()[1])
}

func TestUpdatePageRollsBackTheSlugWhenMenuTargetsFail(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Page", func(value *page.Page) { value.Slug = text("old") })
	menuDown := errors.New("menu targets unavailable")
	h.store.Fail = map[string]error{"UpdateMenuTargets": menuDown}

	_, err := h.service.UpdatePage(h.member(newPrincipal()).context(t), page.UpdatePageCommand{ID: stored.ID, Slug: text("new")})

	require.ErrorIs(t, err, menuDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	unchanged, _ := h.store.Page(stored.ID)
	require.Equal(t, "old", *unchanged.Slug)
	require.Empty(t, h.audit.Records())
}

func TestUpdatePageRejectsATakenSlugAndAMaskedPage(t *testing.T) {
	h := newHarness(t)
	h.seedPage("Holder", func(value *page.Page) { value.Slug = text("taken") })
	stored := h.seedPage("Page", func(value *page.Page) { value.ShowTitle = true })
	_, err := h.service.UpdatePage(h.member(newPrincipal()).context(t), page.UpdatePageCommand{ID: stored.ID, Slug: text("taken")})
	requireCode(t, err, connect.CodeAlreadyExists, "page with slug 'taken' already exists")

	h.store.Fail = map[string]error{"UpdateSettings": page.ErrSlugTaken}
	hidden := false
	_, err = h.service.UpdatePage(h.member(newPrincipal()).context(t), page.UpdatePageCommand{ID: stored.ID, ShowTitle: &hidden})
	requireCode(t, err, connect.CodeAlreadyExists, "page with slug 'slug' already exists")

	manage, err := policyv1.Page.Manage(stored.ID)
	require.NoError(t, err)
	h.checker.Deny(manage, nil)
	_, err = h.service.UpdatePage(h.member(newPrincipal()).context(t), page.UpdatePageCommand{ID: stored.ID, ShowTitle: &hidden})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
}

func TestDeletePageRemovesThePageAndEverythingPointingAtIt(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Doomed", func(value *page.Page) { value.Slug = text("doomed") })
	documentID := h.documentID(t, stored.ID)
	policy, err := policyv1.Page.DeletePolicy(stored.ID)
	require.NoError(t, err)
	h.checker.Snapshot = []policyv1.RelationshipMutation{policy}

	require.NoError(t, h.service.DeletePage(h.member(newPrincipal()).context(t), page.DeletePageCommand{ID: stored.ID}))

	_, exists := h.store.Page(stored.ID)
	require.False(t, exists)
	_, exists = h.store.Document(documentID)
	require.False(t, exists)
	require.Equal(t, []string{stored.ID}, h.store.ShareLinkDeletes())
	require.Equal(t, []string{stored.ID}, h.store.OGCancels())
	require.Equal(t, []string{stored.ID + ":featured_image"}, h.store.Released())
	require.Equal(t, []string{stored.ID}, h.store.MenuRemovals())
	require.Equal(t, [][]policyv1.RelationshipMutation{{policy}}, h.checker.Applied())
	require.Empty(t, h.checker.Compensated())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditPageDeleted}, auditActions(h.audit))
}

func TestDeletePageKeepsThePageWhenAStepFails(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Kept", func(value *page.Page) { value.Slug = text("kept") })
	policy, err := policyv1.Page.DeletePolicy(stored.ID)
	require.NoError(t, err)
	h.checker.Snapshot = []policyv1.RelationshipMutation{policy}
	menuDown := errors.New("menu targets unavailable")
	h.store.Fail = map[string]error{"RemoveMenuTargets": menuDown}

	err = h.service.DeletePage(h.member(newPrincipal()).context(t), page.DeletePageCommand{ID: stored.ID})

	require.ErrorIs(t, err, menuDown)
	_, exists := h.store.Page(stored.ID)
	require.True(t, exists)
	require.Empty(t, h.checker.Applied())
	require.Empty(t, h.audit.Records())
}

func TestDeletePageMasksAPageTheCallerMayNotDelete(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Page")
	deleteAction, err := policyv1.Page.Delete(stored.ID)
	require.NoError(t, err)
	h.checker.Deny(deleteAction, nil)

	err = h.service.DeletePage(h.member(newPrincipal()).context(t), page.DeletePageCommand{ID: stored.ID})

	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
	_, exists := h.store.Page(stored.ID)
	require.True(t, exists)
	err = h.service.DeletePage(h.member(newPrincipal()).context(t), page.DeletePageCommand{ID: uuid.NewString()})
	requireCode(t, err, connect.CodeNotFound, "")
}

func TestPublishPageKeepsTheFirstPublicationAndSkipsANoOp(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Page")
	admin := h.member(newPrincipal())

	published, err := h.service.PublishPage(admin.context(t), page.PublishPageCommand{ID: stored.ID})
	require.NoError(t, err)
	require.True(t, published.Changed)
	require.Equal(t, page.StatusPublished, published.Page.Status)
	first := *published.Page.PublishedAt
	require.Equal(t, []page.ContentUpdate{{PageID: stored.ID, Kind: page.StateUpdate, Fields: []string{"state.status", "state.published_at"}}}, h.store.Updates())

	again, err := h.service.PublishPage(admin.context(t), page.PublishPageCommand{ID: stored.ID})
	require.NoError(t, err)
	require.False(t, again.Changed)
	require.Len(t, h.audit.Records(), 1, "a no-op appends no record")

	_, err = h.service.UnpublishPage(admin.context(t), page.UnpublishPageCommand{ID: stored.ID})
	require.NoError(t, err)
	require.Equal(t, page.ContentUpdate{PageID: stored.ID, Kind: page.StateUpdate, Fields: []string{"state.status"}}, h.store.Updates()[1])
	republished, err := h.service.PublishPage(admin.context(t), page.PublishPageCommand{ID: stored.ID})
	require.NoError(t, err)
	require.True(t, first.Equal(*republished.Page.PublishedAt))
	require.Len(t, h.audit.Records(), 3)
}

func TestPublishPageRequiresReadyBlockMedia(t *testing.T) {
	image, video := uuid.New(), uuid.New()
	for _, test := range []struct {
		name    string
		file    page.PublicationFile
		ready   []page.PresentationKey
		missing bool
		message string
	}{
		{name: "missing attachment", missing: true, message: "Page content Block has a missing media attachment"},
		{name: "file gone", message: "Page content Block media File is unavailable"},
		{name: "image not ready", file: page.PublicationFile{ID: image.String(), MIMEType: "image/webp"}, message: "Page content Block image presentation is not ready"},
		{name: "pending deletion", file: page.PublicationFile{ID: image.String(), MIMEType: "image/webp", DeleteRequested: true}, message: "Page content Block media File is pending deletion"},
		{name: "video without its HLS stream", file: page.PublicationFile{ID: video.String(), MIMEType: "video/mp4", DurationSeconds: new(int)},
			ready: []page.PresentationKey{{FileID: video.String(), Kind: "FILE_DERIVATIVE_TYPE_THUMBNAIL"}}, message: "Page content Block media presentation is not ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			stored := h.seedPage("Page")
			attachment := contentblock.PublicationAttachment{FileID: image}
			if test.file.ID != "" {
				attachment.FileID = uuid.MustParse(test.file.ID)
				h.store.PutFile(test.file)
			}
			if test.missing {
				attachment.MissingMediaKind = "image"
			}
			h.store.PutAttachments(h.documentID(t, stored.ID), attachment)
			for _, key := range test.ready {
				h.store.PutPresentation(key, false)
			}

			_, err := h.service.PublishPage(h.member(newPrincipal()).context(t), page.PublishPageCommand{ID: stored.ID})

			requireCode(t, err, connect.CodeFailedPrecondition, test.message)
			unchanged, _ := h.store.Page(stored.ID)
			require.Equal(t, page.StatusDraft, unchanged.Status)
		})
	}
	t.Run("ready image", func(t *testing.T) {
		h := newHarness(t)
		stored := h.seedPage("Page")
		h.store.PutFile(page.PublicationFile{ID: image.String(), MIMEType: "image/webp"})
		h.store.PutPresentation(page.PresentationKey{FileID: image.String(), Kind: "image"}, true)
		h.store.PutAttachments(h.documentID(t, stored.ID), contentblock.PublicationAttachment{FileID: image})
		_, err := h.service.PublishPage(h.member(newPrincipal()).context(t), page.PublishPageCommand{ID: stored.ID})
		require.NoError(t, err)
	})
}

func TestCheckPageSlugAvailableAnswersInvalidOccupiedAndOwnSlugs(t *testing.T) {
	h := newHarness(t)
	admin := h.member(newPrincipal())
	stored := h.seedPage("Page", func(value *page.Page) { value.Slug = text("mine") })
	h.store.PutRoute("posts/article")
	for slug, want := range map[string]bool{"free": true, "mine": false, "posts/article": false, "../escape": false, "": false} {
		available, err := h.service.CheckPageSlugAvailable(admin.context(t), page.CheckPageSlugAvailableQuery{Slug: slug})
		require.NoError(t, err)
		require.Equal(t, want, available, slug)
	}
	available, err := h.service.CheckPageSlugAvailable(admin.context(t), page.CheckPageSlugAvailableQuery{Slug: "mine", ExcludeID: &stored.ID})
	require.NoError(t, err)
	require.True(t, available)

	manage, err := policyv1.Page.Manage(stored.ID)
	require.NoError(t, err)
	h.checker.Deny(manage, nil)
	_, err = h.service.CheckPageSlugAvailable(admin.context(t), page.CheckPageSlugAvailableQuery{Slug: "mine", ExcludeID: &stored.ID})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
}

func TestFeaturedImageChangesRequestEveryLocaleOGImage(t *testing.T) {
	h := newHarness(t)
	h.store.OGRunID = "run-1"
	stored := h.seedPage("Page")
	fileID := uuid.NewString()
	h.store.PutFile(page.PublicationFile{ID: fileID, MIMEType: "image/webp"})
	admin := h.member(newPrincipal())

	set, err := h.service.SetPageFeaturedImage(admin.context(t), page.SetPageFeaturedImageCommand{PageID: stored.ID, FileID: fileID})
	require.NoError(t, err)
	require.Equal(t, fileID, set.Delivery.FileId)
	require.Equal(t, "run-1", *set.OgGenerationRunID)
	updated, _ := h.store.Page(stored.ID)
	require.Equal(t, fileID, *updated.FeaturedImageFileID)

	_, err = h.service.SetPageFeaturedImage(admin.context(t), page.SetPageFeaturedImageCommand{PageID: stored.ID, FileID: fileID})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), 1, "setting the same File changes nothing")

	runID, err := h.service.DeletePageFeaturedImage(admin.context(t), page.DeletePageFeaturedImageCommand{PageID: stored.ID})
	require.NoError(t, err)
	require.Equal(t, "run-1", *runID)
	updated, _ = h.store.Page(stored.ID)
	require.Nil(t, updated.FeaturedImageFileID)
	require.Equal(t, []string{stored.ID + ":featured_image*"}, h.store.Released())
	require.Equal(t, []pagetest.OGRequest{
		{PageID: stored.ID, AllLocales: true, Reason: "page_featured_image_updated"},
		{PageID: stored.ID, AllLocales: true, Reason: "page_featured_image_removed"},
	}, h.store.OGRequests())
	require.Equal(t, []page.ContentUpdate{{PageID: stored.ID, Kind: page.FeaturedImageUpdate}, {PageID: stored.ID, Kind: page.FeaturedImageUpdate}}, h.store.Updates())

	_, err = h.service.SetPageFeaturedImage(admin.context(t), page.SetPageFeaturedImageCommand{PageID: stored.ID, FileID: uuid.NewString()})
	requireCode(t, err, connect.CodeNotFound, "")
}
