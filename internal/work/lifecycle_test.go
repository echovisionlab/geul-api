package work_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-api/internal/work/worktest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateWorkStoresADraftInTheCreatorsLocale(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	h.store.PutMember(admin.memberID, worktest.Member{Nickname: "Admin", PreferredLocale: stringPointer("ko")})
	h.store.OGRunID = "og-run"

	created, err := h.service.CreateWork(admin.context(t), work.CreateWorkCommand{
		Title: "  Created  ", Type: "WORK_TYPE_ARTICLE", Summary: stringPointer("Summary"), Year: 2026, Month: 8,
		IsPresent: new(true), Metadata: map[string]any{"key": "value", "year": 1999}, AcceptLanguage: "ja",
		Document: emptyDocument("ko"),
	})

	require.NoError(t, err)
	stored := h.stored(t, created.ID)
	require.Equal(t, work.StatusDraft, stored.Status)
	require.Equal(t, "ko", stored.SourceLocale, "the creator's preferred locale wins over the request language")
	require.Equal(t, map[string]any{"key": "value"}, stored.Metadata, "range keys never reach the metadata")
	source, ok := h.store.Locale(created.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "Created", *source.Title)
	require.Equal(t, "Summary", *source.Summary)
	require.Equal(t, "Created", created.Title)
	require.NotEmpty(t, created.Revision)
	require.NotNil(t, created.Document)
	require.Equal(t, []worktest.OGRequest{{WorkID: created.ID, Reason: "work_created"}}, h.store.OGRequests())
	touch, err := policyv1.Work.TouchPolicy(created.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditWorkCreated, records[0].Action)
	require.Equal(t, sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: admin.memberID}, records[0].RecordActor)
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit,
		mutation.StepOutbox, mutation.StepRelationships, mutation.StepCommit,
	}, h.units.Steps())
}

func TestCreateWorkRejectsInvalidInputBeforeItsTransaction(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	h.store.PutRoute("taken-route")
	valid := work.CreateWorkCommand{Title: "Title", Type: "WORK_TYPE_ARTICLE", Year: 2026, Month: 8, IsPresent: new(true)}
	for name, test := range map[string]struct {
		change  func(*work.CreateWorkCommand)
		code    connect.Code
		message string
	}{
		"blank title":        {func(c *work.CreateWorkCommand) { c.Title = "  " }, connect.CodeInvalidArgument, "title is required"},
		"missing type":       {func(c *work.CreateWorkCommand) { c.Type = "" }, connect.CodeInvalidArgument, "type is required"},
		"month out of range": {func(c *work.CreateWorkCommand) { c.Month = 13 }, connect.CodeInvalidArgument, "month: must be between 1 and 12"},
		"open range": {func(c *work.CreateWorkCommand) { c.IsPresent = nil; c.UntilYear = int32Pointer(2026) },
			connect.CodeInvalidArgument, "until_month is required"},
		"range ending first": {func(c *work.CreateWorkCommand) {
			c.IsPresent, c.UntilYear, c.UntilMonth = nil, int32Pointer(2025), int32Pointer(1)
		}, connect.CodeInvalidArgument, "until: must not be earlier than from"},
		"slug a route holds": {func(c *work.CreateWorkCommand) { c.Slug = stringPointer("taken-route") },
			connect.CodeAlreadyExists, "work with slug \"taken-route\" already exists"},
	} {
		t.Run(name, func(t *testing.T) {
			command := valid
			test.change(&command)
			_, err := h.service.CreateWork(admin.context(t), command)
			requireCode(t, err, test.code, test.message)
		})
	}
	require.Empty(t, h.units.Steps(), "no transaction opened")
}

func TestCreateWorkRollsBackAHeldSlugOrAForeignDocument(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	h.seedWork(t, "Holder", func(value *work.Work) { value.Slug = stringPointer("held") })

	_, err := h.service.CreateWork(admin.context(t), work.CreateWorkCommand{
		Title: "Second", Slug: stringPointer("held"), Type: "WORK_TYPE_ARTICLE", Year: 2026, Month: 8, IsPresent: new(true),
	})
	requireCode(t, err, connect.CodeAlreadyExists, "work with slug \"held\" already exists")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))

	_, err = h.service.CreateWork(admin.context(t), work.CreateWorkCommand{
		Title: "Third", Type: "WORK_TYPE_ARTICLE", Year: 2026, Month: 8, IsPresent: new(true), SourceLocale: "en",
		Document: emptyDocument("ko"),
	})
	requireCode(t, err, connect.CodeInvalidArgument, "document.source_locale: must match the server-selected source locale")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	require.Empty(t, h.checker.Applied())
	require.Empty(t, h.audit.Records())
}

func TestCreateWorkRequiresFreshAdministratorAuthority(t *testing.T) {
	create, err := policyv1.Work.Create()
	require.NoError(t, err)
	h := newHarness(t, withChecker(authztest.AllowAll().Deny(create, nil)))
	admin := newPrincipal()

	_, err = h.service.CreateWork(admin.context(t), work.CreateWorkCommand{Title: "Title", Type: "WORK_TYPE_ARTICLE", Year: 2026, Month: 8, IsPresent: new(true)})

	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
}

func TestPublishAndUnpublishMoveTheLifecycleOnce(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Lifecycle")

	published, err := h.service.PublishWork(admin.context(t), work.PublishWorkCommand{ID: value.ID})
	require.NoError(t, err)
	require.True(t, published.Changed)
	require.Equal(t, work.StatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)
	stored := h.stored(t, value.ID)
	require.Equal(t, published.UpdatedAt, stored.UpdatedAt)
	require.Equal(t, *published.PublishedAt, *stored.PublishedAt)
	require.Equal(t, published.UpdatedAt.Truncate(time.Microsecond), published.UpdatedAt,
		"the response carries the instant PostgreSQL stores, so a later read sees no change")

	again, err := h.service.PublishWork(admin.context(t), work.PublishWorkCommand{ID: value.ID})
	require.NoError(t, err)
	require.False(t, again.Changed)

	unpublished, err := h.service.UnpublishWork(admin.context(t), work.UnpublishWorkCommand{ID: value.ID})
	require.NoError(t, err)
	require.True(t, unpublished.Changed)
	require.Equal(t, unpublished.UpdatedAt, h.stored(t, value.ID).UpdatedAt)
	require.Equal(t, unpublished.UpdatedAt.Truncate(time.Microsecond), unpublished.UpdatedAt)
	require.Equal(t, work.StatusDraft, h.stored(t, value.ID).Status)
	require.Equal(t, *published.PublishedAt, *h.stored(t, value.ID).PublishedAt, "unpublishing keeps published_at")

	_, err = h.service.UnpublishWork(admin.context(t), work.UnpublishWorkCommand{ID: value.ID})
	require.NoError(t, err)

	records := h.audit.Records()
	require.Len(t, records, 2, "a no-op lifecycle mutation is not audited")
	require.Equal(t, []work.ContentUpdate{
		{WorkID: value.ID, Kind: work.StateUpdate, Fields: []string{"state.status", "state.published_at"}},
		{WorkID: value.ID, Kind: work.StateUpdate, Fields: []string{"state.status"}},
	}, h.store.Updates())
	publish, err := policyv1.Work.Publish(value.ID)
	require.NoError(t, err)
	require.Contains(t, h.checker.Checks(), authztest.Of(admin.identityID, publish, nil))
}

func TestArchivedWorkAllowsEditsButRestrictsItsLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	publishedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	value := h.seedWork(t, "Archived", func(value *work.Work) {
		value.Status, value.PublishedAt = work.StatusArchived, &publishedAt
	})
	ctx := admin.context(t)

	_, err := h.service.UnpublishWork(ctx, work.UnpublishWorkCommand{ID: value.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "archived works must be published before they can be unpublished")
	err = h.service.DeleteWork(ctx, work.DeleteWorkCommand{ID: value.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "archived works must be published before deletion")
	_, err = h.service.UpdateWork(ctx, work.UpdateWorkCommand{ID: value.ID, Slug: stringPointer("updated-while-archived")})
	require.NoError(t, err)
	_, err = h.service.CreateWorkCreditGroup(ctx, work.CreateWorkCreditGroupCommand{WorkID: value.ID, Name: "archived edits"})
	require.NoError(t, err)

	republished, err := h.service.PublishWork(ctx, work.PublishWorkCommand{ID: value.ID})
	require.NoError(t, err)
	require.Equal(t, work.StatusPublished, republished.Status)
	require.Equal(t, publishedAt, *h.stored(t, value.ID).PublishedAt, "archived to published keeps published_at")
	updates := h.store.Updates()
	require.Equal(t, work.ContentUpdate{WorkID: value.ID, Kind: work.StateUpdate, Fields: []string{"state.status"}}, updates[len(updates)-1],
		"restoring an archived Work does not publish it again")
	editArchived, err := policyv1.Work.EditArchived(value.ID)
	require.NoError(t, err)
	require.Contains(t, h.checker.Checks(), authztest.Of(admin.identityID, editArchived, nil), "an archived Work authorizes EditArchived")
}

func TestWorkMutationsAnswerADeniedOrInactiveCallerAsMissing(t *testing.T) {
	admin := newPrincipal()
	h := newHarness(t)
	value := h.seedWork(t, "Hidden")
	publish, err := policyv1.Work.Publish(value.ID)
	require.NoError(t, err)
	h.checker.Deny(publish, nil)

	_, err = h.service.PublishWork(admin.context(t), work.PublishWorkCommand{ID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+value.ID)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))

	h.fence.Deactivate(admin.identityID)
	_, err = h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{ID: value.ID, Featured: new(true)})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+value.ID)
	require.False(t, h.stored(t, value.ID).Featured)
}

func TestMissingWorkAnswersNotFoundBeforeItsTransaction(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	const missing = "33333333-3333-4333-8333-333333333333"

	_, err := h.service.PublishWork(admin.context(t), work.PublishWorkCommand{ID: missing})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+missing)
	err = h.service.DeleteWork(admin.context(t), work.DeleteWorkCommand{ID: missing})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+missing)
	_, err = h.service.GetWork(admin.context(t), work.GetWorkQuery{ID: missing})
	requireCode(t, err, connect.CodeNotFound, "work not found: "+missing)
	require.Empty(t, h.units.Steps())
}

func TestDeleteWorkReleasesItsDocumentLinksImagesAndPolicy(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Deleted")
	documentID := h.documentID(t, value.ID)

	require.NoError(t, h.service.DeleteWork(admin.context(t), work.DeleteWorkCommand{ID: value.ID}))

	_, ok := h.store.Stored(value.ID)
	require.False(t, ok)
	_, ok = h.store.Document(documentID)
	require.False(t, ok)
	require.Equal(t, []string{value.ID}, h.store.ShareLinkDeletes())
	require.Equal(t, []string{value.ID}, h.store.OGCancels())
	remove, err := policyv1.Work.DeletePolicy(value.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{remove}}, h.checker.Applied())
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditWorkDeleted, records[0].Action)
	require.Equal(t, sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: admin.memberID}, records[0].RecordActor)
}

func TestDeleteWorkRollsBackAFailedDeleteWithoutWritingItsPolicy(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Kept")
	h.store.Fail = map[string]error{"DeleteWork": errStoreDown}

	err := h.service.DeleteWork(admin.context(t), work.DeleteWorkCommand{ID: value.ID})

	require.ErrorIs(t, err, errStoreDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	_, ok := h.store.Stored(value.ID)
	require.True(t, ok)
	require.Empty(t, h.checker.Applied(), "a failed change writes no relationship")
}
