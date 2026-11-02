package programevent_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func createCommand(eventType programevent.Type) programevent.CreateProgramEventCommand {
	return programevent.CreateProgramEventCommand{
		Title: " Opening night ", Slug: "opening-" + uuid.NewString(), SourceLocale: "ko", TypeID: eventType.ID,
		StartsAt: timePtr(at(60)), EndsAt: timePtr(at(120)), Timezone: "Asia/Seoul",
		LocationMode: programevent.LocationModeOnline, MapPlaceID: text("ignored for online"), Summary: text("Summary"),
	}
}

func TestCreateProgramEventStoresTheDraftItsDocumentAndPolicy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	admin := newPrincipal()
	member := uuid.NewString()
	h.store.PutMember(member)
	artist, label, client := uuid.NewString(), uuid.NewString(), uuid.NewString()
	poster := uuid.NewString()
	command := createCommand(h.seedType(t, "concert"))
	command.PosterFileID = &poster
	command.Artists = []programevent.Relation{{ID: artist, Role: text("headliner")}}
	command.Labels = []programevent.Relation{{ID: label, SortOrder: 1}}
	command.Clients = []programevent.Relation{{ID: client}}
	command.Credits = []programevent.Credit{{MemberID: &member, CreditRole: text(" host ")}, {DisplayName: text("Crew"), SortOrder: 1}}

	created, err := h.service.CreateProgramEvent(admin.context(t), command)

	require.NoError(t, err)
	require.Equal(t, "Opening night", created.Title)
	require.Equal(t, programevent.StatusDraft, created.Status)
	require.Equal(t, "ko", created.SourceLocale)
	require.Nil(t, created.MapPlaceID, "an online event keeps no Map Place")
	require.Equal(t, poster, *created.PosterFileID)
	require.Equal(t, []programevent.Relation{{ID: artist, Role: text("headliner")}}, created.Artists)
	require.Equal(t, []programevent.Relation{{ID: label, SortOrder: 1}}, created.Labels)
	require.Len(t, created.Credits, 2)
	require.Equal(t, "host", *created.Credits[0].CreditRole)
	require.Equal(t, []programevent.LocaleView{{Locale: "ko", Summary: text("Summary")}}, created.Locales)
	require.NotEmpty(t, created.DocumentRevision)
	require.Equal(t, []string{poster}, h.store.LockedFiles())
	require.Equal(t, map[string]programeventtestBinding{"media_" + created.Media[0].ID: {FileID: poster}}, bindings(h, "program_event", created.ID))

	touch, err := policyv1.ProgramEvent.TouchPolicy(created.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditProgramEventCreated, records[0].Action)
	require.Equal(t, created.ID, records[0].TargetID)
	can, err := policyv1.ProgramEvent.Create()
	require.NoError(t, err)
	require.Contains(t, h.checker.Checks(), authztest.Of(admin.identityID, can, nil))
}

func TestCreateProgramEventValidatesBeforeItWrites(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		change  func(*programevent.CreateProgramEventCommand)
		message string
	}{
		"title":    {func(c *programevent.CreateProgramEventCommand) { c.Title = " " }, "title is required"},
		"slug":     {func(c *programevent.CreateProgramEventCommand) { c.Slug = "Upper" }, "slug: can only contain lowercase letters, numbers, and hyphens"},
		"start":    {func(c *programevent.CreateProgramEventCommand) { c.StartsAt = nil }, "starts_at is required"},
		"range":    {func(c *programevent.CreateProgramEventCommand) { c.EndsAt = timePtr(at(0)) }, "ends_at: must be greater than or equal to starts_at"},
		"timezone": {func(c *programevent.CreateProgramEventCommand) { c.Timezone = "" }, "timezone is required"},
		"map place": {func(c *programevent.CreateProgramEventCommand) {
			c.LocationMode, c.MapPlaceID = programevent.LocationModeHybrid, nil
		}, "map_place_id is required"},
		"unknown mode":  {func(c *programevent.CreateProgramEventCommand) { c.LocationMode = "" }, "location_mode: unsupported location mode"},
		"source locale": {func(c *programevent.CreateProgramEventCommand) { c.SourceLocale = "xx" }, "source_locale: unsupported locale"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			command := createCommand(h.seedType(t, "concert"))
			test.change(&command)
			_, err := h.service.CreateProgramEvent(newPrincipal().context(t), command)
			requireCode(t, err, connect.CodeInvalidArgument, test.message)
			require.Empty(t, h.units.Steps(), "no transaction opened")
		})
	}
}

func TestCreateProgramEventRejectsAnOccupiedRouteAndATakenSlug(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	command := createCommand(h.seedType(t, "concert"))
	h.store.OccupyRoute(programevent.EventRoute, command.Slug)
	_, err := h.service.CreateProgramEvent(newPrincipal().context(t), command)
	requireCode(t, err, connect.CodeAlreadyExists, "")

	existing := h.seedEvent(t)
	command.Slug = existing.Slug
	_, err = h.service.CreateProgramEvent(newPrincipal().context(t), command)
	requireCode(t, err, connect.CodeAlreadyExists, "program event with slug '"+existing.Slug+"' already exists")
	require.ErrorIs(t, err, programevent.ErrSlugTaken)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	require.Empty(t, h.checker.Applied(), "no policy is written for a failed create")
}

func TestCreateProgramEventResolvesTheSourceLocaleFromThePreferenceThenTheRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	admin := newPrincipal()
	command := createCommand(h.seedType(t, "concert"))
	command.SourceLocale, command.AcceptLanguage = "", "ja"
	created, err := h.service.CreateProgramEvent(admin.context(t), command)
	require.NoError(t, err)
	require.Equal(t, "ja", created.SourceLocale)

	h.store.PreferredLocales = map[string]string{admin.memberID: "ko"}
	command.Slug = "preferred-" + uuid.NewString()
	created, err = h.service.CreateProgramEvent(admin.context(t), command)
	require.NoError(t, err)
	require.Equal(t, "ko", created.SourceLocale)

	h.store.PreferredLocales = nil
	h.store.Fail = map[string]error{"TranslationSettings": errors.New("settings down")}
	command.Slug, command.AcceptLanguage = "default-"+uuid.NewString(), ""
	created, err = h.service.CreateProgramEvent(admin.context(t), command)
	require.NoError(t, err)
	require.Equal(t, "en", created.SourceLocale)
}

func TestCreateProgramEventRollsBackWhenTheAuditFails(t *testing.T) {
	t.Parallel()
	auditDown := errors.New("audit down")
	h := newHarness(t, withAudit(&audittest.Recorder{Err: auditDown}))
	command := createCommand(h.seedType(t, "concert"))
	_, err := h.service.CreateProgramEvent(newPrincipal().context(t), command)
	require.ErrorIs(t, err, auditDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	events, total, listErr := h.store.ListEvents(t.Context(), programevent.ListPage{Limit: 10})
	require.NoError(t, listErr)
	require.Zero(t, total)
	require.Empty(t, events)
	require.Empty(t, h.checker.Applied(), "the audit fails before the policy is written")
}

func TestGetProgramEventMasksWhatTheCallerMayNotView(t *testing.T) {
	t.Parallel()
	viewer := newPrincipal()
	h := newHarness(t, withChecker(&authztest.Checker{}))
	event := h.seedEvent(t)
	_, err := h.service.GetProgramEvent(viewer.context(t), programevent.GetProgramEventQuery{ID: event.ID})
	requireCode(t, err, connect.CodeNotFound, "program event not found: "+event.ID)

	view, err := policyv1.ProgramEvent.View(event.ID)
	require.NoError(t, err)
	h.checker.Allow(view, nil)
	got, err := h.service.GetProgramEvent(viewer.context(t), programevent.GetProgramEventQuery{ID: event.ID})
	require.NoError(t, err)
	require.Equal(t, event.ID, got.ID)
	require.Equal(t, []string{viewer.identityID}, hydratedIdentities(h))

	h.store.Fence.Deactivate(viewer.identityID)
	_, err = h.service.GetProgramEvent(viewer.context(t), programevent.GetProgramEventQuery{ID: event.ID})
	requireCode(t, err, connect.CodeNotFound, "program event not found")

	_, err = h.service.GetProgramEvent(viewer.context(t), programevent.GetProgramEventQuery{ID: uuid.NewString()})
	requireCode(t, err, connect.CodeNotFound, "")
}

func TestGetProgramEventOfAnArchivedEventChecksViewArchived(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	viewer := newPrincipal()
	event := h.seedEvent(t, func(event *programevent.Event) { event.Status = programevent.StatusArchived })
	_, err := h.service.GetProgramEvent(viewer.context(t), programevent.GetProgramEventQuery{ID: event.ID})
	require.NoError(t, err)
	viewArchived, err := policyv1.ProgramEvent.ViewArchived(event.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(viewer.identityID, viewArchived, nil)}, h.checker.Checks())
}

func TestGetProgramEventProjectsTheStoredEvent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	admin := newPrincipal()
	member, artist := uuid.NewString(), uuid.NewString()
	h.members.summaries[member] = &memberref.Summary{ID: member, Nickname: "Host"}
	h.store.CreditArtists = map[string]programevent.CreditArtist{artist: {ID: artist, Name: "Artist", Slug: text("artist")}}
	h.store.ArtistImages = map[string]string{artist: "artist-image"}
	h.store.ReadyImages = map[string]*mediaasset.Reference{"artist-image": {AssetID: "image-asset"}}
	event := h.seedEvent(t)
	h.store.PutLocale(event.ID, programevent.LocaleMetadata{Locale: "ko", Summary: text("요약"), UpdatedAt: at(1)})
	require.NoError(t, h.store.InsertCredit(t.Context(), &programevent.CreditRecord{EventID: event.ID, ArtistID: &artist, SortOrder: 1}))
	require.NoError(t, h.store.InsertCredit(t.Context(), &programevent.CreditRecord{EventID: event.ID, MemberID: &member, SortOrder: 0}))
	missingArtist := uuid.NewString()
	require.NoError(t, h.store.InsertCredit(t.Context(), &programevent.CreditRecord{EventID: event.ID, ArtistID: &missingArtist, SortOrder: 2}))

	got, err := h.service.GetProgramEvent(admin.context(t), programevent.GetProgramEventQuery{ID: event.ID})

	require.NoError(t, err)
	require.Equal(t, []string{"en", "ko"}, []string{got.Locales[0].Locale, got.Locales[1].Locale}, "the source locale leads")
	require.Equal(t, "요약", *got.Locales[1].Summary)
	require.Len(t, got.Credits, 3)
	require.Equal(t, h.members.summaries[member], got.Credits[0].Member)
	require.Equal(t, &programevent.CreditArtist{ID: artist, Name: "Artist", Slug: text("artist"), ImageAsset: &mediaasset.Reference{AssetID: "image-asset"}}, got.Credits[1].Artist)
	require.Nil(t, got.Credits[2].Artist, "an unreadable artist is left out")
	require.Nil(t, got.PosterFileID)
}

func TestListProgramEventsAdminPagesSummariesWithTheirPosters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	first := h.seedEvent(t, func(event *programevent.Event) { event.StartsAt = at(10) })
	second := h.seedEvent(t, func(event *programevent.Event) { event.StartsAt = at(20) })
	poster := programevent.Media{EventID: first.ID, FileID: uuid.NewString(), Role: "poster", IsPrimary: true}
	require.NoError(t, h.store.InsertMedia(t.Context(), &poster))

	list, err := h.service.ListProgramEventsAdmin(newPrincipal().context(t), programevent.ListProgramEventsQuery{Limit: 1, Offset: 1})

	require.NoError(t, err)
	require.Equal(t, programevent.Pagination{Total: 2, Limit: 1, Offset: 1}, list.Pagination)
	require.Len(t, list.Events, 1)
	require.Equal(t, first.ID, list.Events[0].ID)
	require.Equal(t, poster.FileID, *list.Events[0].PosterFileID)

	list, err = h.service.ListProgramEventsAdmin(newPrincipal().context(t), programevent.ListProgramEventsQuery{})
	require.NoError(t, err)
	require.Equal(t, programevent.Pagination{Total: 2, Limit: 50}, list.Pagination)
	require.Equal(t, second.ID, list.Events[0].ID)
	require.Nil(t, list.Events[0].PosterFileID)

	denied := newHarness(t, withChecker(&authztest.Checker{}))
	_, err = denied.service.ListProgramEventsAdmin(newPrincipal().context(t), programevent.ListProgramEventsQuery{})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
}

func TestDeleteProgramEventRemovesTheEventItsDocumentAndPolicy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	media := programevent.Media{EventID: event.ID, FileID: uuid.NewString(), Role: "poster", IsPrimary: true}
	require.NoError(t, h.store.InsertMedia(t.Context(), &media))
	require.NoError(t, h.store.BindPosterMedia(t.Context(), event.ID, media.ID, media.FileID))

	require.NoError(t, h.service.DeleteProgramEvent(newPrincipal().context(t), programevent.DeleteProgramEventCommand{ID: event.ID}))

	_, ok := h.store.StoredEvent(event.ID)
	require.False(t, ok)
	_, ok = h.store.StoredDocument(uuid.MustParse(*event.ContentDocumentID))
	require.False(t, ok)
	require.Empty(t, bindings(h, "program_event", event.ID))
	deletePolicy, err := policyv1.ProgramEvent.DeletePolicy(event.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{deletePolicy}}, h.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditProgramEventDeleted}, auditActions(h.audit))

	err = h.service.DeleteProgramEvent(newPrincipal().context(t), programevent.DeleteProgramEventCommand{ID: event.ID})
	requireCode(t, err, connect.CodeNotFound, "program event not found: "+event.ID)
}

func TestDeleteProgramEventChecksDeleteOnTheLockedEvent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withChecker(&authztest.Checker{}))
	caller := newPrincipal()
	event := h.seedEvent(t, func(event *programevent.Event) { event.Status = programevent.StatusArchived })
	err := h.service.DeleteProgramEvent(caller.context(t), programevent.DeleteProgramEventCommand{ID: event.ID})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit_archived this program event")
	editArchived, err := policyv1.ProgramEvent.EditArchived(event.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(caller.identityID, editArchived, nil)}, h.checker.Checks())
	require.Equal(t, []mutationStep{mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
}

type mutationStep = mutation.Step

func TestPublishAndArchiveMoveTheLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	admin := newPrincipal()
	event := h.seedEvent(t)

	published, err := h.service.PublishProgramEvent(admin.context(t), programevent.PublishProgramEventCommand{ID: event.ID})
	require.NoError(t, err)
	require.Equal(t, programevent.StatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)
	require.True(t, published.Changed)

	_, err = h.service.PublishProgramEvent(admin.context(t), programevent.PublishProgramEventCommand{ID: event.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "program event is already published")

	archived, err := h.service.ArchiveProgramEvent(admin.context(t), programevent.ArchiveProgramEventCommand{ID: event.ID})
	require.NoError(t, err)
	require.Equal(t, programevent.StatusArchived, archived.Status)
	require.Equal(t, published.PublishedAt, archived.PublishedAt)

	_, err = h.service.ArchiveProgramEvent(admin.context(t), programevent.ArchiveProgramEventCommand{ID: event.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "only published program events can be archived")

	restored, err := h.service.PublishProgramEvent(admin.context(t), programevent.PublishProgramEventCommand{ID: event.ID})
	require.NoError(t, err)
	require.Equal(t, published.PublishedAt, restored.PublishedAt, "restoring keeps the original publication date")

	records := h.audit.Records()
	require.Len(t, records, 3)
	require.Equal(t, sharedtelemetry.AuditStateDraft, records[0].PreviousState)
	require.Equal(t, sharedtelemetry.AuditStatePublished, records[0].NewState)
	require.Equal(t, sharedtelemetry.AuditStateArchived, records[1].NewState)
	require.Equal(t, sharedtelemetry.AuditStateArchived, records[2].PreviousState)

	_, err = h.service.PublishProgramEvent(admin.context(t), programevent.PublishProgramEventCommand{ID: uuid.NewString()})
	requireCode(t, err, connect.CodeNotFound, "")
}

func TestPublishingAnArchivedEventNeedsEditArchived(t *testing.T) {
	t.Parallel()
	caller := newPrincipal()
	h := newHarness(t, withChecker(&authztest.Checker{}))
	event := h.seedEvent(t, func(event *programevent.Event) { event.Status = programevent.StatusArchived })
	publish, err := policyv1.ProgramEvent.Publish(event.ID)
	require.NoError(t, err)
	h.checker.Allow(publish, nil)
	_, err = h.service.PublishProgramEvent(caller.context(t), programevent.PublishProgramEventCommand{ID: event.ID})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit_archived this program event")

	h.store.Fence.Deactivate(caller.identityID)
	_, err = h.service.PublishProgramEvent(caller.context(t), programevent.PublishProgramEventCommand{ID: event.ID})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to publish this program event")
}

// programeventtestBinding names the fake's binding in assertions.
type programeventtestBinding = struct{ FileID string }

func bindings(h *harness, owner, ownerID string) map[string]programeventtestBinding {
	result := map[string]programeventtestBinding{}
	for key, binding := range h.store.Bindings(owner, ownerID) {
		result[key] = programeventtestBinding{FileID: binding.FileID}
	}
	return result
}

func hydratedIdentities(h *harness) []string {
	var identities []string
	for _, principal := range h.store.Hydrated {
		identities = append(identities, principal.IdentityID.String())
	}
	return identities
}
