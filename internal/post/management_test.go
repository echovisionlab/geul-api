package post_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/post/posttest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func text(value string) *string { return &value }

// audits are the actions and changed fields of recorder's records.
func audits(recorder *audittest.Recorder) []string {
	var described []string
	for _, record := range recorder.Records() {
		described = append(described, string(record.Action)+" "+strings.Join(record.ChangedFields, ","))
	}
	return described
}

func TestCreatePostStoresThePostItsAuthorAndItsPolicy(t *testing.T) {
	h := newHarness(t)
	creator := h.member(newPrincipal(), "Creator")
	h.store.PutTaxonomy(post.Categories, "category-1")
	h.store.PutTaxonomy(post.Tags, "tag-1")

	view, err := h.service.CreatePost(creator.context(t), post.CreatePostCommand{
		Title: "  Created  ", Slug: text("created"), Summary: text("Summary"), SourceLocale: "en",
		CategoryIDs: []string{"category-1"}, TagIDs: []string{"tag-1"}, CommentsEnabled: true,
	})

	require.NoError(t, err)
	require.Equal(t, "Created", view.Title)
	require.Equal(t, post.StatusDraft, view.Status)
	stored, ok := h.store.Post(view.ID)
	require.True(t, ok)
	require.Equal(t, "created", *stored.Slug)
	require.True(t, stored.CommentsEnabled)
	require.NotNil(t, stored.ContentDocumentID)
	_, ok = h.store.Document(uuid.MustParse(*stored.ContentDocumentID))
	require.True(t, ok, "the Post owns a new content document")
	locale, ok := h.store.Locale(view.ID, "en")
	require.True(t, ok)
	require.Equal(t, "Created", *locale.Title)
	require.Equal(t, "Summary", *locale.Summary)
	_, isAuthor := h.store.Participant(view.ID, creator.memberID, post.RoleAuthor)
	require.True(t, isAuthor)
	require.Equal(t, []string{"category-1"}, h.store.Links(post.Categories, view.ID))
	require.Equal(t, []string{"tag-1"}, h.store.Links(post.Tags, view.ID))

	policy, err := policyv1.Post.TouchPolicy(view.ID)
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(creator.identityID)
	require.NoError(t, err)
	author, err := policyv1.Post.TouchAuthor(view.ID, actor)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{policy, author}}, h.checker.Applied())
	require.Equal(t, []string{string(sharedtelemetry.AuditPostCreated) + " "}, audits(h.audit))
	require.Equal(t, []posttest.OGRequest{{PostID: view.ID, Locale: "en", Reason: "post_created"}}, h.store.OGRequests())
	require.Equal(t, mutation.StepCommit, lastStep(t, h.units))
}

func TestCreatePostRejectsTakenSlugsAndUnknownReferences(t *testing.T) {
	for name, test := range map[string]struct {
		seed    func(*harness)
		command post.CreatePostCommand
		code    connect.Code
	}{
		"a Post slug":         {seed: func(h *harness) { h.seedPost("Taken", "", func(value *post.Post) { value.Slug = text("taken") }) }, command: post.CreatePostCommand{Slug: text("taken")}, code: connect.CodeAlreadyExists},
		"a Page route":        {seed: func(h *harness) { h.store.PutRoute("taken") }, command: post.CreatePostCommand{Slug: text("taken")}, code: connect.CodeAlreadyExists},
		"a missing Category":  {command: post.CreatePostCommand{CategoryIDs: []string{"missing"}}, code: connect.CodeInvalidArgument},
		"a missing Map Place": {command: post.CreatePostCommand{MapPlaceID: text(uuid.NewString())}, code: connect.CodeInvalidArgument},
		"no title":            {command: post.CreatePostCommand{Title: " "}, code: connect.CodeInvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			creator := h.member(newPrincipal(), "Creator")
			if test.seed != nil {
				test.seed(h)
			}
			command := test.command
			if command.Title == "" {
				command.Title = "Rejected"
			}
			command.SourceLocale = "en"

			_, err := h.service.CreatePost(creator.context(t), command)

			requireCode(t, err, test.code, "")
			require.Empty(t, h.checker.Applied())
			require.Empty(t, h.audit.Records())
			require.Empty(t, h.store.OGRequests())
		})
	}
}

func TestCreatePostLeavesNothingWhenItsRelationshipsFail(t *testing.T) {
	h := newHarness(t, withChecker(authztest.AllowAll().FailWrites(errDown)))
	creator := h.member(newPrincipal(), "Creator")

	_, err := h.service.CreatePost(creator.context(t), post.CreatePostCommand{Title: "Lost", SourceLocale: "en"})

	require.ErrorIs(t, err, errDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	page, total, err := h.store.ListPosts(t.Context(), post.ListQuery{Limit: 10})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, page)
}

func TestCreatePostNeedsTheCreatePermission(t *testing.T) {
	h := newHarness(t, withChecker(&authztest.Checker{}))
	creator := h.member(newPrincipal(), "Creator")

	_, err := h.service.CreatePost(creator.context(t), post.CreatePostCommand{Title: "Denied", SourceLocale: "en"})

	requireCode(t, err, connect.CodePermissionDenied, "")
	require.Empty(t, h.checker.Applied())
}

func TestUpdatePostKeepsTheConfigurationRevisionContract(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Configured", "", func(value *post.Post) { value.Slug = text("configured") })
	ctx := editor.context(t)
	unchanged, enabled, disabled := false, true, false

	_, err := h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: value.ID, CommentsEnabled: &unchanged})
	requireCode(t, err, connect.CodeFailedPrecondition, "Post configuration revision is required; reload before saving")
	_, err = h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: value.ID, CommentsEnabled: &unchanged, ExpectedConfigurationRevision: "not-a-uuid"})
	requireCode(t, err, connect.CodeInvalidArgument, "")

	noOp, err := h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: value.ID, CommentsEnabled: &unchanged, ExpectedConfigurationRevision: value.ConfigurationRevision})
	require.NoError(t, err)
	require.False(t, noOp.Changed)
	require.Equal(t, value.ConfigurationRevision, noOp.ConfigurationRevision)
	require.Empty(t, h.audit.Records(), "a no-op is not audited")

	first, err := h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: value.ID, CommentsEnabled: &enabled, ExpectedConfigurationRevision: value.ConfigurationRevision})
	require.NoError(t, err)
	require.True(t, first.Changed)
	require.True(t, first.CommentsEnabled)
	require.NotEqual(t, value.ConfigurationRevision, first.ConfigurationRevision)
	require.Equal(t, []string{string(sharedtelemetry.AuditPostUpdated) + " comments_enabled"}, audits(h.audit))

	_, err = h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: value.ID, Slug: text("stale"), ExpectedConfigurationRevision: value.ConfigurationRevision})
	requireCode(t, err, connect.CodeAborted, "Post configuration changed; reload before saving")
	stored, _ := h.store.Post(value.ID)
	require.Equal(t, "configured", *stored.Slug, "a stale writer changes nothing")

	off, err := h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: value.ID, CommentsEnabled: &disabled, ExpectedConfigurationRevision: first.ConfigurationRevision})
	require.NoError(t, err)
	on, err := h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: value.ID, CommentsEnabled: &enabled, ExpectedConfigurationRevision: off.ConfigurationRevision})
	require.NoError(t, err)
	require.NotEqual(t, first.ConfigurationRevision, on.ConfigurationRevision, "returning to an earlier value takes a new revision")
	require.Empty(t, h.store.Updates(), "settings publish no content wake-up")
}

func TestUpdatePostValidatesTheSlugTheMapPlaceAndTheLayout(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Validated", "")
	h.store.PutRoute("page-route")
	mapPlaceID := uuid.NewString()
	h.store.PutMapPlace(mapPlaceID)
	ctx := editor.context(t)
	update := func(change func(*post.UpdatePostCommand)) (post.SettingsUpdate, error) {
		stored, _ := h.store.Post(value.ID)
		command := post.UpdatePostCommand{ID: value.ID, ExpectedConfigurationRevision: stored.ConfigurationRevision}
		change(&command)
		return h.service.UpdatePost(ctx, command)
	}

	_, err := update(func(command *post.UpdatePostCommand) { command.Slug = text("nested/slug") })
	requireCode(t, err, connect.CodeInvalidArgument, "", "a Post slug takes no slash")
	_, err = update(func(command *post.UpdatePostCommand) { command.Slug = text("page-route") })
	requireCode(t, err, connect.CodeAlreadyExists, "")
	_, err = update(func(command *post.UpdatePostCommand) { command.MapPlaceID = text(uuid.NewString()) })
	requireCode(t, err, connect.CodeInvalidArgument, "map_place_id: map place does not exist")
	_, err = update(func(command *post.UpdatePostCommand) {
		command.DocumentLayout = &post.DocumentLayout{ContentHeight: "TALL", PageChrome: post.PlacementFlow, Footer: post.PlacementFlow}
	})
	requireCode(t, err, connect.CodeInvalidArgument, "content_height must be CONTENT or VIEWPORT")
	_, err = h.service.UpdatePost(ctx, post.UpdatePostCommand{ID: uuid.NewString(), ExpectedConfigurationRevision: value.ConfigurationRevision})
	requireCode(t, err, connect.CodeNotFound, "")
	require.Empty(t, h.audit.Records())

	layout := post.DocumentLayout{ContentHeight: post.ContentHeightViewport, PageChrome: post.PlacementPinned, Footer: post.PlacementFlow}
	updated, err := update(func(command *post.UpdatePostCommand) {
		command.Slug, command.MapPlaceID, command.DocumentLayout = text("fresh"), &mapPlaceID, &layout
	})
	require.NoError(t, err)
	require.Equal(t, "fresh", *updated.Slug)
	require.Equal(t, mapPlaceID, *updated.MapPlaceID)
	require.Equal(t, layout, updated.DocumentLayout)
	require.Equal(t, []string{string(sharedtelemetry.AuditPostUpdated) + " document_layout,map_place_id,slug"}, audits(h.audit))

	cleared, err := update(func(command *post.UpdatePostCommand) { command.Slug, command.MapPlaceID = text(" "), text("") })
	require.NoError(t, err)
	require.Nil(t, cleared.Slug)
	require.Nil(t, cleared.MapPlaceID)
}

func TestUpdatePostNeedsTheEditPermission(t *testing.T) {
	viewer := newPrincipal()
	checker := &authztest.Checker{}
	h := newHarness(t, withChecker(checker))
	h.member(viewer, "Viewer")
	value := h.seedPost("Guarded", "")
	checker.For(viewer.identityID).Allow(policyv1.Post.View(value.ID))
	enabled := true

	_, err := h.service.UpdatePost(viewer.context(t), post.UpdatePostCommand{ID: value.ID, CommentsEnabled: &enabled, ExpectedConfigurationRevision: value.ConfigurationRevision})

	requireCode(t, err, connect.CodePermissionDenied, "")
	stored, _ := h.store.Post(value.ID)
	require.False(t, stored.CommentsEnabled)
}

func TestLifecycleMovesFollowTheirStatusRules(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Lifecycle", "")
	ctx := editor.context(t)
	command := post.PostLifecycleCommand{ID: value.ID}

	_, err := h.service.ArchivePost(ctx, command)
	requireCode(t, err, connect.CodeFailedPrecondition, "only published posts can be archived")
	_, err = h.service.RepublishPost(ctx, command)
	requireCode(t, err, connect.CodeFailedPrecondition, "only archived posts can be republished")
	_, err = h.service.UnpublishPost(ctx, command)
	requireCode(t, err, connect.CodeFailedPrecondition, "only published or archived posts can be unpublished")

	published, err := h.service.PublishPost(ctx, command)
	require.NoError(t, err)
	require.Equal(t, post.StatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)
	publishedAt := *published.PublishedAt
	_, err = h.service.PublishPost(ctx, command)
	requireCode(t, err, connect.CodeFailedPrecondition, "")

	archived, err := h.service.ArchivePost(ctx, command)
	require.NoError(t, err)
	require.Equal(t, post.StatusArchived, archived.Status)
	draft, err := h.service.UnpublishPost(ctx, command)
	require.NoError(t, err)
	require.Equal(t, post.StatusDraft, draft.Status, "an archived Post returns straight to draft")
	require.Equal(t, publishedAt, *draft.PublishedAt, "unpublishing keeps the first publication time")
	republished, err := h.service.PublishPost(ctx, command)
	require.NoError(t, err)
	require.Equal(t, publishedAt, *republished.PublishedAt, "publishing again keeps the first publication time")
	_, err = h.service.ArchivePost(ctx, command)
	require.NoError(t, err)
	_, err = h.service.RepublishPost(ctx, command)
	require.NoError(t, err)

	var transitions []string
	for _, record := range h.audit.Records() {
		require.Equal(t, sharedtelemetry.AuditPostUpdated, record.Action)
		transitions = append(transitions, string(record.PreviousState)+">"+string(record.NewState))
	}
	require.Equal(t, []string{
		"draft>published", "published>archived", "archived>draft", "draft>published", "published>archived", "archived>published",
	}, transitions)
	stored, _ := h.store.Post(value.ID)
	require.Equal(t, value.ConfigurationRevision, stored.ConfigurationRevision, "lifecycle moves keep the configuration revision")
}

func TestLifecycleMovesNeedThePublishPermission(t *testing.T) {
	editor := newPrincipal()
	checker := &authztest.Checker{}
	h := newHarness(t, withChecker(checker))
	h.member(editor, "Editor")
	value := h.seedPost("Guarded", "")
	checker.For(editor.identityID).Allow(policyv1.Post.View(value.ID))
	checker.For(editor.identityID).Allow(policyv1.Post.Edit(value.ID))

	_, err := h.service.PublishPost(editor.context(t), post.PostLifecycleCommand{ID: value.ID})

	requireCode(t, err, connect.CodePermissionDenied, "")
	stored, _ := h.store.Post(value.ID)
	require.Equal(t, post.StatusDraft, stored.Status)
	require.Empty(t, h.audit.Records())
}

func TestScheduleAndCancelAPost(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Scheduled", "")
	ctx := editor.context(t)
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Minute)
	past := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Minute)

	_, err := h.service.SchedulePost(ctx, post.SchedulePostCommand{ID: value.ID, TimeZone: "Asia/Seoul"})
	requireCode(t, err, connect.CodeInvalidArgument, "")
	_, err = h.service.SchedulePost(ctx, post.SchedulePostCommand{ID: value.ID, ScheduledAt: &future, TimeZone: "Mars/Olympus"})
	requireCode(t, err, connect.CodeInvalidArgument, "")
	_, err = h.service.SchedulePost(ctx, post.SchedulePostCommand{ID: value.ID, ScheduledAt: &past, TimeZone: "Asia/Seoul"})
	requireCode(t, err, connect.CodeInvalidArgument, "")

	scheduled, err := h.service.SchedulePost(ctx, post.SchedulePostCommand{ID: value.ID, ScheduledAt: &future, TimeZone: "Asia/Seoul"})
	require.NoError(t, err)
	require.Equal(t, post.StatusScheduled, scheduled.Status)
	require.Equal(t, future, scheduled.ScheduledAt.UTC())
	require.Equal(t, "Asia/Seoul", *scheduled.ScheduledTimeZone)

	cancelled, err := h.service.CancelPostSchedule(ctx, post.PostLifecycleCommand{ID: value.ID})
	require.NoError(t, err)
	require.Equal(t, post.StatusDraft, cancelled.Status)
	require.Nil(t, cancelled.ScheduledAt)
	stored, _ := h.store.Post(value.ID)
	require.Nil(t, stored.ScheduledTimeZone)
	require.Len(t, h.audit.Records(), 2)
}

func TestProcessDueScheduledPostsPublishesOnlyDuePosts(t *testing.T) {
	h := newHarness(t)
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	due := h.seedPost("Due", "", func(value *post.Post) { value.Status, value.ScheduledAt = post.StatusScheduled, &past })
	later := h.seedPost("Later", "", func(value *post.Post) { value.Status, value.ScheduledAt = post.StatusScheduled, &future })
	h.seedPost("Draft", "")

	published, err := post.ProcessDueScheduledPosts(t.Context(), h.store, 10)

	require.NoError(t, err)
	require.Equal(t, []string{due.ID}, published)
	stored, _ := h.store.Post(due.ID)
	require.Equal(t, post.StatusPublished, stored.Status)
	require.NotNil(t, stored.PublishedAt)
	stored, _ = h.store.Post(later.ID)
	require.Equal(t, post.StatusScheduled, stored.Status)
}

func TestFeaturedImageIsSetAndRemoved(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Featured", "")
	fileID := uuid.NewString()
	h.store.PutFile(fileID)
	delivery := h.files.put(fileID)
	h.store.OGRunID = "og-run"
	ctx := editor.context(t)

	_, err := h.service.SetPostFeaturedImage(ctx, post.SetPostFeaturedImageCommand{PostID: value.ID, FileID: uuid.NewString()})
	requireCode(t, err, connect.CodeNotFound, "")
	require.Empty(t, h.store.OGRequests())

	featured, err := h.service.SetPostFeaturedImage(ctx, post.SetPostFeaturedImageCommand{PostID: value.ID, FileID: fileID})
	require.NoError(t, err)
	require.Equal(t, delivery, featured.Delivery)
	require.Equal(t, "og-run", *featured.OgRunID)
	stored, _ := h.store.Post(value.ID)
	require.Equal(t, fileID, *stored.FeaturedImageFileID)
	require.Equal(t, []string{fileID}, h.store.LockedFiles())

	removed, err := h.service.DeletePostFeaturedImage(ctx, post.DeletePostFeaturedImageCommand{PostID: value.ID})
	require.NoError(t, err)
	require.NotNil(t, removed)
	stored, _ = h.store.Post(value.ID)
	require.Nil(t, stored.FeaturedImageFileID)

	require.Equal(t, []posttest.OGRequest{
		{PostID: value.ID, Reason: "post_featured_image_updated", Force: true},
		{PostID: value.ID, Reason: "post_featured_image_removed", Force: true},
	}, h.store.OGRequests())
	var operations []sharedtelemetry.AuditCollectionOperation
	for _, record := range h.audit.Records() {
		require.Equal(t, fileID, record.AssetID)
		operations = append(operations, record.CollectionOperation)
	}
	require.Equal(t, []sharedtelemetry.AuditCollectionOperation{sharedtelemetry.AuditCollectionOperationAdded, sharedtelemetry.AuditCollectionOperationRemoved}, operations)
}

// postRelationships stores the policy and author relationships a Post's
// deletion snapshot finds, and returns them.
func postRelationships(t *testing.T, h *harness, postID string, author principal, extra int) []policyv1.RelationshipMutation {
	t.Helper()
	policy, err := policyv1.Post.TouchPolicy(postID)
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(author.identityID)
	require.NoError(t, err)
	authorRelation, err := policyv1.Post.TouchAuthor(postID, actor)
	require.NoError(t, err)
	stored := []policyv1.RelationshipMutation{policy, authorRelation}
	for range extra {
		collaborator, err := policyv1.NewAccountIdentityActor(uuid.NewString())
		require.NoError(t, err)
		relationship, err := policyv1.Post.TouchCollaborator(postID, collaborator)
		require.NoError(t, err)
		stored = append(stored, relationship)
	}
	h.checker.Relationships[postID] = stored
	return stored
}

func TestDeletePostRemovesItsRowsRelationshipsAndOutboundWork(t *testing.T) {
	h := newHarness(t)
	author := h.member(newPrincipal(), "Author")
	value := h.seedPost("Deleted", author.memberID)
	postRelationships(t, h, value.ID, author, 0)

	err := h.service.DeletePost(author.context(t), post.DeletePostCommand{ID: value.ID})

	require.NoError(t, err)
	_, ok := h.store.Post(value.ID)
	require.False(t, ok)
	_, ok = h.store.Document(uuid.MustParse(*value.ContentDocumentID))
	require.False(t, ok, "the content document goes with the Post")
	require.Equal(t, []string{value.ID}, h.store.ShareLinkDeletions())
	require.Equal(t, []string{value.ID}, h.store.OGCancels())
	require.Len(t, h.checker.Applied(), 1)
	require.Len(t, h.checker.Applied()[0], 2, "the policy and the author relationship are deleted")
	require.Equal(t, []string{string(sharedtelemetry.AuditPostDeleted) + " "}, audits(h.audit))
}

func TestDeletePostRefusesWhatItCannotDeleteAtomically(t *testing.T) {
	for name, test := range map[string]struct {
		change  func(*post.Post)
		extra   int
		missing bool
		code    connect.Code
		message string
	}{
		"an archived Post": {change: func(value *post.Post) { value.Status = post.StatusArchived }, code: connect.CodeFailedPrecondition, message: "archived posts must be republished before deletion"},
		"an oversized authorization snapshot": {extra: 999, code: connect.CodeFailedPrecondition,
			message: "post has too many authorization relationships to delete atomically; remove participant relationships or reparent dependent resources first"},
		"missing relationships": {missing: true, code: connect.CodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			author := h.member(newPrincipal(), "Author")
			var change []func(*post.Post)
			if test.change != nil {
				change = append(change, test.change)
			}
			value := h.seedPost("Kept", author.memberID, change...)
			if !test.missing {
				postRelationships(t, h, value.ID, author, test.extra)
			}

			err := h.service.DeletePost(author.context(t), post.DeletePostCommand{ID: value.ID})

			requireCode(t, err, test.code, test.message)
			_, ok := h.store.Post(value.ID)
			require.True(t, ok)
			require.Empty(t, h.checker.Applied())
			require.Empty(t, h.store.ShareLinkDeletions())
			require.Empty(t, h.audit.Records())
		})
	}
}

func TestCheckSlugAvailableAnswersForTheSignedInMember(t *testing.T) {
	h := newHarness(t)
	member := h.member(newPrincipal(), "Member")
	value := h.seedPost("Owned", "", func(value *post.Post) { value.Slug = text("owned") })
	h.store.PutRoute("page-route")
	ctx := member.context(t)

	_, err := h.service.CheckSlugAvailable(t.Context(), post.CheckSlugAvailableQuery{Slug: "free"})
	requireCode(t, err, connect.CodeUnauthenticated, "")
	for slug, want := range map[string]bool{"free": true, "owned": false, "page-route": false} {
		available, err := h.service.CheckSlugAvailable(ctx, post.CheckSlugAvailableQuery{Slug: slug})
		require.NoError(t, err)
		require.Equal(t, want, available, slug)
	}
	available, err := h.service.CheckSlugAvailable(ctx, post.CheckSlugAvailableQuery{Slug: "owned", ExcludePostID: &value.ID})
	require.NoError(t, err)
	require.True(t, available, "a Post keeps its own slug")
}

func TestGetPostHidesWhatTheReaderMayNotView(t *testing.T) {
	reader := newPrincipal()
	checker := &authztest.Checker{}
	h := newHarness(t, withChecker(checker))
	h.member(reader, "Reader")
	value := h.seedPost("Hidden", "")
	ctx := reader.context(t)

	_, err := h.service.GetPost(ctx, post.GetPostQuery{ID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "")

	checker.For(reader.identityID).Allow(policyv1.Post.View(value.ID))
	view, err := h.service.GetPost(ctx, post.GetPostQuery{ID: value.ID})
	require.NoError(t, err)
	require.Equal(t, "Hidden", view.Title)
	require.Equal(t, value.ConfigurationRevision, view.ConfigurationRevision)
	require.NotContains(t, view.Actions, post.ActionEdit)
}

func TestGetPostResolvesTheFeaturedImageOnlyForAReader(t *testing.T) {
	reader := newPrincipal()
	checker := &authztest.Checker{}
	h := newHarness(t, withChecker(checker))
	h.member(reader, "Reader")
	fileID := uuid.NewString()
	delivery := h.files.put(fileID)
	value := h.seedPost("Featured", "", func(value *post.Post) { value.FeaturedImageFileID = &fileID })

	_, err := h.service.GetPost(t.Context(), post.GetPostQuery{ID: value.ID})
	require.Error(t, err, "a public reader cannot enter the manage read")
	_, err = h.service.GetPost(reader.context(t), post.GetPostQuery{ID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "")
	require.Empty(t, h.files.resolved, "a denied read never reaches the File signer")

	checker.For(reader.identityID).Allow(policyv1.Post.View(value.ID))
	view, err := h.service.GetPost(reader.context(t), post.GetPostQuery{ID: value.ID})
	require.NoError(t, err)
	require.Equal(t, delivery, view.FeaturedImage)
	require.Equal(t, [][2]string{{value.ID, fileID}}, h.files.resolved, "the File is resolved against its owning Post")
}
