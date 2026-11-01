package programevent_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/programevent/programeventtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestUpdateProgramEventWritesTheChangedSettingsAndAuditsTheirFields(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t, func(event *programevent.Event) { event.TicketURL = text("https://old.example.com") })
	newType, slug, timezone := uuid.NewString(), "renamed-"+uuid.NewString(), "Asia/Seoul"

	update, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{
		ID: event.ID, TypeID: &newType, Slug: &slug, Timezone: &timezone, StartsAt: timePtr(at(70)),
		TicketURL: text(" "), StreamURL: text(" https://stream.example.com "), AllDay: boolPtr(false),
	})

	require.NoError(t, err)
	require.True(t, update.Changed)
	stored, _ := h.store.StoredEvent(event.ID)
	require.Equal(t, newType, stored.TypeID)
	require.Equal(t, slug, stored.Slug)
	require.Nil(t, stored.TicketURL, "an empty URL clears it")
	require.Equal(t, "https://stream.example.com", *stored.StreamURL)
	require.Equal(t, update.UpdatedAt, stored.UpdatedAt)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditProgramEventUpdated, records[0].Action)
	require.Equal(t, []string{"slug", "starts_at", "stream_url", "ticket_url", "timezone", "type"}, records[0].ChangedFields)
}

func TestUpdateProgramEventThatChangesNothingIsUnaudited(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	update, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{
		ID: event.ID, Timezone: text("UTC"), StartsAt: timePtr(event.StartsAt),
		Artists: programevent.RelationsEdit{Replace: true, Observed: &[]programevent.Relation{}},
	})
	require.NoError(t, err)
	require.False(t, update.Changed)
	require.Equal(t, event.UpdatedAt, update.UpdatedAt)
	require.Empty(t, h.audit.Records())
	require.Equal(t, mutation.StepCommit, lastStep(t, h.units))
}

func TestUpdateProgramEventRechecksTimeAndLocationAgainstTheLockedEvent(t *testing.T) {
	t.Parallel()
	t.Run("a concurrent end moves the requested start out of range", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		event := h.seedEvent(t, func(event *programevent.Event) { event.EndsAt = timePtr(at(600)) })
		h.store.Before = map[string]func(*programeventtest.Store){"Event": concurrentChange(event.ID, func(event *programevent.Event) {
			event.EndsAt = timePtr(at(90))
		})}
		_, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{ID: event.ID, StartsAt: timePtr(at(120))})
		requireCode(t, err, connect.CodeInvalidArgument, "ends_at: must be greater than or equal to starts_at")
	})
	t.Run("a concurrent move online keeps the Map Place cleared", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		place := uuid.NewString()
		event := h.seedEvent(t, func(event *programevent.Event) {
			event.LocationMode, event.MapPlaceID = programevent.LocationModeMapPlace, &place
		})
		h.store.Before = map[string]func(*programeventtest.Store){"Event": concurrentChange(event.ID, func(event *programevent.Event) {
			event.LocationMode, event.MapPlaceID = programevent.LocationModeOnline, nil
		})}
		requested := uuid.NewString()
		_, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{ID: event.ID, MapPlaceID: &requested})
		require.NoError(t, err)
		stored, _ := h.store.StoredEvent(event.ID)
		require.Equal(t, programevent.LocationModeOnline, stored.LocationMode)
		require.Nil(t, stored.MapPlaceID)
	})
}

// concurrentChange plays a writer that changes the event between the
// pre-read and the locked read of an update: the hook fires on the second
// read.
func concurrentChange(eventID string, change func(*programevent.Event)) func(*programeventtest.Store) {
	reads := 0
	return func(store *programeventtest.Store) {
		reads++
		if reads != 2 {
			return
		}
		event, _ := store.StoredEvent(eventID)
		change(&event)
		store.PutEvent(event)
	}
}

func TestUpdateProgramEventMergesRelationsIntoTheLockedRows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	kept, removed, peer, added := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, h.store.InsertRelations(t.Context(), programevent.ArtistRelation, event.ID, []programevent.Relation{
		{ID: kept, SortOrder: 0}, {ID: removed, SortOrder: 1}, {ID: peer, Role: text("added by a peer"), SortOrder: 4},
	}, at(0)))
	observed := []programevent.Relation{{ID: kept}, {ID: removed}}

	update, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{
		ID: event.ID, Artists: programevent.RelationsEdit{
			Replace: true, Observed: &observed,
			Desired: []programevent.Relation{{ID: kept, Role: text("host")}, {ID: added}},
		},
	})

	require.NoError(t, err)
	require.True(t, update.Changed)
	require.Equal(t, []programevent.Relation{
		{ID: kept, Role: text("host"), SortOrder: 0}, {ID: peer, Role: text("added by a peer"), SortOrder: 4}, {ID: added, SortOrder: 5},
	}, h.store.StoredRelations(programevent.ArtistRelation, event.ID))
	require.Equal(t, []string{"artists"}, h.audit.Records()[0].ChangedFields)

	_, err = h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{
		ID: event.ID, Labels: programevent.RelationsEdit{Desired: []programevent.Relation{{ID: added}}},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "observed_labels: is required when updating labels")
}

func TestUpdateProgramEventReplacesCreditsAndAuditsEachChange(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	kept := programevent.CreditRecord{EventID: event.ID, DisplayName: text("Kept"), SortOrder: 0}
	removed := programevent.CreditRecord{EventID: event.ID, DisplayName: text("Removed"), SortOrder: 1}
	require.NoError(t, h.store.InsertCredit(t.Context(), &kept))
	require.NoError(t, h.store.InsertCredit(t.Context(), &removed))

	_, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{
		ID: event.ID, Credits: programevent.CreditsEdit{Replace: true, Desired: []programevent.Credit{
			{DisplayName: text("New"), SortOrder: 0}, {ID: kept.ID, DisplayName: text("Kept"), CreditRole: text("director"), SortOrder: 1},
		}},
	})

	require.NoError(t, err)
	credits := h.store.StoredCredits(event.ID)
	require.Len(t, credits, 2)
	require.Equal(t, "New", *credits[0].DisplayName)
	require.Equal(t, kept.ID, credits[1].ID)
	require.Equal(t, "director", *credits[1].CreditRole)
	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range h.audit.Records() {
		operations = append(operations, record.ItemOperation)
	}
	require.Equal(t, []sharedtelemetry.AuditItemOperation{
		sharedtelemetry.AuditItemOperationCreated, sharedtelemetry.AuditItemOperationUpdated, sharedtelemetry.AuditItemOperationDeleted, "",
	}, operations, "each credit, then the new order")
	require.Equal(t, &[]string{credits[0].ID, credits[1].ID}, h.audit.Records()[3].ItemIDs)

	_, err = h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{
		ID: event.ID, Credits: programevent.CreditsEdit{Replace: true, Desired: []programevent.Credit{{ID: uuid.NewString(), DisplayName: text("Foreign")}}},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "credits: credit id does not belong to program event")

	_, err = h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{
		ID: event.ID, Credits: programevent.CreditsEdit{Replace: true, Desired: []programevent.Credit{{DisplayName: text("Both"), MemberID: text(uuid.NewString())}}},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "credit: exactly one of artist_id, user_id, display_name is required")
}

func TestUpdateProgramEventPosterAddsAndRemovesThePrimary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	poster := uuid.NewString()
	_, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{ID: event.ID, PosterFileID: &poster})
	require.NoError(t, err)
	media := h.store.StoredMedia(event.ID)
	require.Len(t, media, 1)
	require.True(t, media[0].IsPrimary)
	require.Equal(t, map[string]programeventtestBinding{"media_" + media[0].ID: {FileID: poster}}, bindings(h, "program_event", event.ID))

	_, err = h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{ID: event.ID, PosterFileID: text("")})
	require.NoError(t, err)
	require.Empty(t, h.store.StoredMedia(event.ID))
	require.Empty(t, bindings(h, "program_event", event.ID))
	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, records[0].CollectionOperation)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, records[1].CollectionOperation)
	require.Equal(t, poster, records[1].FileID)
}

func TestUpdateProgramEventValidatesTheSeriesRelation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	series := h.store.PutSeries(programevent.Series{Slug: "series", Status: programevent.StatusDraft, Title: "Series"})
	taken := h.seedEvent(t, func(event *programevent.Event) { event.SeriesID, event.SeriesOrder = &series.ID, int32Ptr(1) })
	event := h.seedEvent(t)
	for message, command := range map[string]programevent.UpdateProgramEventCommand{
		"series_id: series_id is required when series_order is set":    {ID: event.ID, SeriesOrder: int32Ptr(1)},
		"series_order: series_order is required when series_id is set": {ID: event.ID, SeriesID: &series.ID},
		"series_order: must be non-negative":                           {ID: event.ID, SeriesID: &series.ID, SeriesOrder: int32Ptr(-1)},
		"series_id: program event series does not exist":               {ID: event.ID, SeriesID: text(uuid.NewString()), SeriesOrder: int32Ptr(1)},
		"series_order: must be unique within the program event series": {ID: event.ID, SeriesID: &series.ID, SeriesOrder: int32Ptr(1)},
	} {
		_, err := h.service.UpdateProgramEvent(newPrincipal().context(t), command)
		requireCode(t, err, connect.CodeInvalidArgument, message)
	}
	_, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{ID: taken.ID, SeriesID: &series.ID, SeriesOrder: int32Ptr(1)})
	require.NoError(t, err, "an event keeps its own order")
}

func TestUpdateProgramEventAnswersAMissingEventAndATakenSlug(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	missing := uuid.NewString()
	_, err := h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{ID: missing})
	requireCode(t, err, connect.CodeNotFound, "program event not found: "+missing)

	first, second := h.seedEvent(t), h.seedEvent(t)
	_, err = h.service.UpdateProgramEvent(newPrincipal().context(t), programevent.UpdateProgramEventCommand{ID: second.ID, Slug: &first.Slug})
	requireCode(t, err, connect.CodeAlreadyExists, "program event with slug '"+first.Slug+"' already exists")
}

func TestUpdateProgramEventChecksEditOnTheLockedLifecycle(t *testing.T) {
	t.Parallel()
	caller := newPrincipal()
	h := newHarness(t, withChecker(&authztest.Checker{}))
	event := h.seedEvent(t)
	h.store.Before = map[string]func(*programeventtest.Store){"Event": concurrentChange(event.ID, func(event *programevent.Event) {
		event.Status = programevent.StatusArchived
	})}
	edit, err := policyv1.ProgramEvent.Edit(event.ID)
	require.NoError(t, err)
	h.checker.Allow(edit, nil)
	_, err = h.service.UpdateProgramEvent(caller.context(t), programevent.UpdateProgramEventCommand{ID: event.ID, Timezone: text("Asia/Seoul")})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit_archived this program event")
	editArchived, err := policyv1.ProgramEvent.EditArchived(event.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(caller.identityID, editArchived, nil)}, h.checker.Checks())

	h.store.Fence.Deactivate(caller.identityID)
	h.store.Before = nil
	_, err = h.service.UpdateProgramEvent(caller.context(t), programevent.UpdateProgramEventCommand{ID: event.ID, Timezone: text("Asia/Seoul")})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit this program event")
}
