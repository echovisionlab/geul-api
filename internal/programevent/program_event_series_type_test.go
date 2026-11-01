package programevent_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/programevent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateProgramEventSeriesBindsItsPosterAndWritesItsPolicy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	poster := uuid.NewString()
	series, err := h.service.CreateProgramEventSeries(newPrincipal().context(t), programevent.CreateProgramEventSeriesCommand{
		Title: " Festival ", Slug: "festival", PosterFileID: &poster,
	})
	require.NoError(t, err)
	require.Equal(t, "Festival", series.Title)
	require.Equal(t, programevent.StatusDraft, series.Status)
	require.Equal(t, map[string]programeventtestBinding{"poster": {FileID: poster}}, bindings(h, "program_event_series", series.ID))
	require.Equal(t, []string{poster}, h.store.LockedFiles())
	touch, err := policyv1.ProgramEventSeries.TouchPolicy(series.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditProgramEventSeriesCreated}, auditActions(h.audit))

	_, err = h.service.CreateProgramEventSeries(newPrincipal().context(t), programevent.CreateProgramEventSeriesCommand{Title: "Again", Slug: "festival"})
	requireCode(t, err, connect.CodeAlreadyExists, "program event series with slug 'festival' already exists")
	h.store.OccupyRoute(programevent.SeriesRoute, "occupied")
	_, err = h.service.CreateProgramEventSeries(newPrincipal().context(t), programevent.CreateProgramEventSeriesCommand{Title: "Page", Slug: "occupied"})
	requireCode(t, err, connect.CodeAlreadyExists, "")
}

func TestUpdateProgramEventSeriesAuditsPosterFieldsAndLifecycleInOrder(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	admin := newPrincipal()
	oldPoster, newPoster := uuid.NewString(), uuid.NewString()
	series := h.store.PutSeries(programevent.Series{Slug: "series", Status: programevent.StatusDraft, Title: "Series", PosterFileID: &oldPoster})
	published := programevent.StatusPublished

	updated, err := h.service.UpdateProgramEventSeries(admin.context(t), programevent.UpdateProgramEventSeriesCommand{
		ID: series.ID, Title: text("Renamed"), Summary: text("Summary"), PosterFileID: &newPoster, Status: &published,
	})

	require.NoError(t, err)
	require.Equal(t, "Renamed", updated.Title)
	require.Equal(t, programevent.StatusPublished, updated.Status)
	require.Equal(t, newPoster, *updated.PosterFileID)
	records := h.audit.Records()
	require.Len(t, records, 3)
	require.Equal(t, newPoster, records[0].FileID)
	require.Equal(t, []string{"summary", "title"}, records[1].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditStatePublished, records[2].NewState)
	publish, err := policyv1.ProgramEventSeries.Publish(series.ID)
	require.NoError(t, err)
	require.Contains(t, h.checker.Checks(), authztest.Of(admin.identityID, publish, nil))

	removed, err := h.service.UpdateProgramEventSeries(newPrincipal().context(t), programevent.UpdateProgramEventSeriesCommand{ID: series.ID, PosterFileID: text("")})
	require.NoError(t, err)
	require.Nil(t, removed.PosterFileID)
	require.Empty(t, bindings(h, "program_event_series", series.ID))
	last := h.audit.Records()[len(h.audit.Records())-1]
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, last.CollectionOperation)
	require.Equal(t, newPoster, last.FileID)

	recorded := len(h.audit.Records())
	_, err = h.service.UpdateProgramEventSeries(newPrincipal().context(t), programevent.UpdateProgramEventSeriesCommand{ID: series.ID, Title: text("Renamed")})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), recorded, "an unchanged series is unaudited")
}

func TestUpdateProgramEventSeriesRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	taken := h.store.PutSeries(programevent.Series{Slug: "taken", Status: programevent.StatusDraft, Title: "Taken"})
	series := h.store.PutSeries(programevent.Series{Slug: "series", Status: programevent.StatusDraft, Title: "Series"})
	archived := programevent.StatusArchived
	_, err := h.service.UpdateProgramEventSeries(newPrincipal().context(t), programevent.UpdateProgramEventSeriesCommand{ID: series.ID, Status: &archived})
	requireCode(t, err, connect.CodeInvalidArgument, "status: must be draft or published")
	_, err = h.service.UpdateProgramEventSeries(newPrincipal().context(t), programevent.UpdateProgramEventSeriesCommand{ID: series.ID, Title: text(" ")})
	requireCode(t, err, connect.CodeInvalidArgument, "title is required")
	_, err = h.service.UpdateProgramEventSeries(newPrincipal().context(t), programevent.UpdateProgramEventSeriesCommand{ID: series.ID, Slug: text(" taken ")})
	requireCode(t, err, connect.CodeAlreadyExists, "program event series with slug ' taken ' already exists")
	_, err = h.service.UpdateProgramEventSeries(newPrincipal().context(t), programevent.UpdateProgramEventSeriesCommand{ID: uuid.NewString()})
	requireCode(t, err, connect.CodeNotFound, "")
	stored, _ := h.store.StoredSeries(taken.ID)
	require.Equal(t, "taken", stored.Slug)
}

func TestDeleteProgramEventSeriesDetachesItsEventsWithoutDeletingThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	poster := uuid.NewString()
	series := h.store.PutSeries(programevent.Series{Slug: "series", Status: programevent.StatusDraft, Title: "Series", PosterFileID: &poster})
	require.NoError(t, h.store.BindSeriesPoster(t.Context(), series.ID, poster))
	member := h.seedEvent(t, func(event *programevent.Event) { event.SeriesID, event.SeriesOrder = &series.ID, int32Ptr(3) })
	other := h.seedEvent(t)

	require.NoError(t, h.service.DeleteProgramEventSeries(newPrincipal().context(t), programevent.DeleteProgramEventSeriesCommand{ID: series.ID}))

	detached, ok := h.store.StoredEvent(member.ID)
	require.True(t, ok, "the event stays")
	require.Nil(t, detached.SeriesID)
	require.Nil(t, detached.SeriesOrder)
	_, ok = h.store.StoredEvent(other.ID)
	require.True(t, ok)
	_, ok = h.store.StoredSeries(series.ID)
	require.False(t, ok)
	require.Empty(t, bindings(h, "program_event_series", series.ID))
	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.AuditProgramEventUpdated, records[0].Action)
	require.Equal(t, member.ID, records[0].TargetID)
	require.Equal(t, []string{"series"}, records[0].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditProgramEventSeriesDeleted, records[1].Action)
	deletePolicy, err := policyv1.ProgramEventSeries.DeletePolicy(series.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{deletePolicy}}, h.checker.Applied())

	err = h.service.DeleteProgramEventSeries(newPrincipal().context(t), programevent.DeleteProgramEventSeriesCommand{ID: series.ID})
	requireCode(t, err, connect.CodeNotFound, "program event series not found: "+series.ID)
}

func TestGetProgramEventSeriesAcceptsTheIDOrTheSlugOfAnAdministrator(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	series := h.store.PutSeries(programevent.Series{ID: uuid.NewString(), Slug: "canonical", Status: programevent.StatusDraft, Title: "Series"})
	byID, err := h.service.GetProgramEventSeries(newPrincipal().context(t), programevent.GetProgramEventSeriesQuery{IDOrSlug: series.ID})
	require.NoError(t, err)
	bySlug, err := h.service.GetProgramEventSeries(newPrincipal().context(t), programevent.GetProgramEventSeriesQuery{IDOrSlug: "canonical"})
	require.NoError(t, err)
	require.Equal(t, byID, bySlug)
	_, err = h.service.GetProgramEventSeries(newPrincipal().context(t), programevent.GetProgramEventSeriesQuery{IDOrSlug: "missing"})
	requireCode(t, err, connect.CodeNotFound, "program event series not found: missing")

	denied := newHarness(t, withChecker(&authztest.Checker{}))
	denied.store.PutSeries(series)
	_, err = denied.service.GetProgramEventSeries(newPrincipal().context(t), programevent.GetProgramEventSeriesQuery{IDOrSlug: series.ID})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
}

func TestListProgramEventSeriesAdminFiltersBySeriesStatus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	published := h.store.PutSeries(programevent.Series{Slug: "published", Status: programevent.StatusPublished, Title: "Published"})
	h.store.PutSeries(programevent.Series{Slug: "draft", Status: programevent.StatusDraft, Title: "Draft"})
	list, err := h.service.ListProgramEventSeriesAdmin(newPrincipal().context(t), programevent.ListProgramEventSeriesQuery{
		Limit: 1000, Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "PROGRAM_EVENT_SERIES_STATUS_PUBLISHED"}},
	})
	require.NoError(t, err)
	require.Len(t, list.Series, 1)
	require.Equal(t, published.ID, list.Series[0].ID)
	require.Equal(t, programevent.Pagination{Total: 1, Limit: 100}, list.Pagination, "the page size is capped")
	_, err = h.service.ListProgramEventSeriesAdmin(newPrincipal().context(t), programevent.ListProgramEventSeriesQuery{
		Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "PROGRAM_EVENT_STATUS_ARCHIVED"}},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "status: must be draft or published")
}

func TestCreateProgramEventTypeStoresItsFirstLocale(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	created, err := h.service.CreateProgramEventType(newPrincipal().context(t), programevent.CreateProgramEventTypeCommand{
		Slug: "concert", AcceptLanguage: "ko", Name: " 공연 ", SortOrder: 2, RequiresPlace: true,
	})
	require.NoError(t, err)
	require.Equal(t, programevent.TypeStatusActive, created.Status)
	require.True(t, created.RequiresPlace)
	require.Equal(t, []programevent.TypeLocale{{TypeID: created.ID, Locale: "ko", Name: "공연", CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt}}, created.Locales)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditProgramEventTypeCreated}, auditActions(h.audit))

	_, err = h.service.CreateProgramEventType(newPrincipal().context(t), programevent.CreateProgramEventTypeCommand{Slug: "concert", Locale: "en", Name: "Concert"})
	requireCode(t, err, connect.CodeAlreadyExists, "program event type with slug 'concert' already exists")
	_, err = h.service.CreateProgramEventType(newPrincipal().context(t), programevent.CreateProgramEventTypeCommand{Slug: "invalid/nested", Locale: "en", Name: "Nested"})
	requireCode(t, err, connect.CodeInvalidArgument, "")
	_, err = h.service.CreateProgramEventType(newPrincipal().context(t), programevent.CreateProgramEventTypeCommand{Slug: "nameless", Locale: "en"})
	requireCode(t, err, connect.CodeInvalidArgument, "name is required")
}

func TestUpdateProgramEventTypeAuditsOnlyItsConfiguration(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	eventType := h.seedType(t, "concert")
	inactive := programevent.TypeStatusInactive

	updated, err := h.service.UpdateProgramEventType(newPrincipal().context(t), programevent.UpdateProgramEventTypeCommand{
		ID: eventType.ID, Status: &inactive, RequiresStreamURL: boolPtr(true), Locale: "ko", Name: text("공연"),
	})
	require.NoError(t, err)
	require.Equal(t, programevent.TypeStatusInactive, updated.Status)
	require.Len(t, updated.Locales, 2)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, []string{"requires_stream_url", "status"}, records[0].ChangedFields)

	_, err = h.service.UpdateProgramEventType(newPrincipal().context(t), programevent.UpdateProgramEventTypeCommand{
		ID: eventType.ID, Locale: "en", Description: text("Live"),
	})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), 1, "a locale edit is not audited")
	require.Equal(t, "Live", *h.store.StoredTypeLocales(eventType.ID)[0].Description)

	for message, command := range map[string]programevent.UpdateProgramEventTypeCommand{
		"status: must be active or inactive": {ID: eventType.ID, Status: new(programevent.TypeStatus)},
		"locale is required":                 {ID: eventType.ID, Name: text("Name")},
		"name is required":                   {ID: eventType.ID, Locale: "ja", Description: text("Only a description")},
		"id is required":                     {},
	} {
		_, err := h.service.UpdateProgramEventType(newPrincipal().context(t), command)
		requireCode(t, err, connect.CodeInvalidArgument, message)
	}
}

func TestDeleteProgramEventTypeKeepsATypeInUse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	used := h.seedType(t, "used")
	h.seedEvent(t, func(event *programevent.Event) { event.TypeID = used.ID })
	err := h.service.DeleteProgramEventType(newPrincipal().context(t), programevent.DeleteProgramEventTypeCommand{ID: used.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "program event type is in use")

	unused := h.seedType(t, "unused")
	require.NoError(t, h.service.DeleteProgramEventType(newPrincipal().context(t), programevent.DeleteProgramEventTypeCommand{ID: unused.ID}))
	_, ok := h.store.StoredType(unused.ID)
	require.False(t, ok)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditProgramEventTypeDeleted}, auditActions(h.audit))
	err = h.service.DeleteProgramEventType(newPrincipal().context(t), programevent.DeleteProgramEventTypeCommand{ID: unused.ID})
	requireCode(t, err, connect.CodeNotFound, "program event type not found: "+unused.ID)
}

func TestListProgramEventTypesAdminPagesTypesWithTheirLocales(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedType(t, "b")
	first := h.seedType(t, "a")
	list, err := h.service.ListProgramEventTypesAdmin(newPrincipal().context(t), programevent.ListProgramEventTypesQuery{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, programevent.Pagination{Total: 2, Limit: 1, HasMore: true}, list.Pagination)
	require.Equal(t, first.ID, list.Types[0].ID)
	require.Len(t, list.Types[0].Locales, 1)
}
