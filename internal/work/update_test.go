package work_test

import (
	"time"

	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/work"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestUpdateWorkMergesSettingsAgainstTheObservedBaseline(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	for _, id := range []string{"client-a", "client-b", "client-c", "client-peer"} {
		h.store.PutClient(work.ClientRow{ID: id, Name: id})
	}
	value := h.seedWork(t, "Settings", func(value *work.Work) {
		value.Metadata = map[string]any{"keep": "peer", "nested": map[string]any{"a": 1.0, "b": 2.0}}
	})
	h.store.LinkClients(value.ID, "client-a", "client-peer", "client-b")

	updated, err := h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{
		ID:               value.ID,
		Metadata:         map[string]any{"nested": map[string]any{"a": 1.0, "b": 3.0}, "added": true},
		ObservedMetadata: map[string]any{"nested": map[string]any{"a": 1.0, "b": 2.0}},
		Clients:          &[]string{"client-a", "client-c", "client-b"},
		ObservedClients:  &[]string{"client-a", "client-b"},
		Featured:         new(true),
	})

	require.NoError(t, err)
	require.True(t, updated.Changed)
	stored := h.stored(t, value.ID)
	require.Equal(t, map[string]any{"keep": "peer", "nested": map[string]any{"a": 1.0, "b": 3.0}, "added": true}, stored.Metadata,
		"a peer's key the caller never observed survives")
	require.True(t, stored.Featured)
	require.Equal(t, []string{"client-a", "client-peer", "client-c", "client-b"}, h.store.StoredClientIDs(value.ID),
		"the inserted client keeps its requested place and the peer's client stays")
	require.Equal(t, []string{"client-a", "client-peer", "client-c", "client-b"}, clientIDs(updated.Clients))
	require.Equal(t, stored.UpdatedAt, updated.Work.UpdatedAt)
	require.Equal(t, updated.Work.UpdatedAt.Truncate(time.Microsecond), updated.Work.UpdatedAt,
		"the response carries the instant PostgreSQL stores, so a later read sees no change")
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditWorkUpdated, records[0].Action)
	require.Equal(t, []string{"clients", "featured", "metadata"}, records[0].ChangedFields)
	require.Equal(t, []work.ContentUpdate{{WorkID: value.ID, Kind: work.SettingsUpdate, Fields: []string{"metadata", "featured", "clients"}}}, h.store.Updates())
}

func TestUpdateWorkWithoutAChangeIsUnchanged(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Unchanged", func(value *work.Work) {
		value.Featured, value.Type = true, "WORK_TYPE_ARTICLE"
	})

	updated, err := h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{
		ID: value.ID, Featured: new(true), Type: stringPointer("WORK_TYPE_ARTICLE"), Year: int32Pointer(2026),
	})

	require.NoError(t, err)
	require.False(t, updated.Changed)
	require.Equal(t, value.UpdatedAt, h.stored(t, value.ID).UpdatedAt)
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.Updates())
}

func TestUpdateWorkRequiresTheBaselineOfAMergedSetting(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Baseline")

	_, err := h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{ID: value.ID, Metadata: map[string]any{}})
	requireCode(t, err, connect.CodeInvalidArgument, "observed_metadata: is required when updating metadata")
	_, err = h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{ID: value.ID, Clients: &[]string{}})
	requireCode(t, err, connect.CodeInvalidArgument, "observed_clients: is required when updating clients")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
}

func TestUpdateWorkValidatesClientsRangeAndPlace(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	h.store.PutClient(work.ClientRow{ID: "client-a", Name: "A"})
	h.store.PutMapPlace("place-1")
	value := h.seedWork(t, "Validated")

	for name, test := range map[string]struct {
		command work.UpdateWorkCommand
		code    connect.Code
		message string
	}{
		"unknown client": {work.UpdateWorkCommand{Clients: &[]string{"missing"}, ObservedClients: &[]string{}},
			connect.CodeInvalidArgument, "clients: contains unknown client id"},
		"blank client": {work.UpdateWorkCommand{Clients: &[]string{" "}, ObservedClients: &[]string{}},
			connect.CodeInvalidArgument, "clients: contains empty client id"},
		"end before the locked start": {work.UpdateWorkCommand{IsPresent: new(false), UntilYear: int32Pointer(2026), UntilMonth: int32Pointer(1)},
			connect.CodeInvalidArgument, "until: must not be earlier than from"},
		"missing place": {work.UpdateWorkCommand{MapPlaceID: stringPointer("place-2")},
			connect.CodeInvalidArgument, "map_place_id: map place does not exist"},
	} {
		t.Run(name, func(t *testing.T) {
			command := test.command
			command.ID = value.ID
			_, err := h.service.UpdateWork(admin.context(t), command)
			requireCode(t, err, test.code, test.message)
		})
	}
	require.Empty(t, h.store.StoredClientIDs(value.ID))

	updated, err := h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{
		ID: value.ID, MapPlaceID: stringPointer(" place-1 "), IsPresent: new(false), UntilYear: int32Pointer(2027), UntilMonth: int32Pointer(2),
	})
	require.NoError(t, err)
	stored := h.stored(t, value.ID)
	require.Equal(t, "place-1", *stored.MapPlaceID)
	require.Equal(t, int32(2027), *stored.UntilYear)
	require.False(t, stored.IsPresent)
	require.Equal(t, stored, updated.Work)
}

func TestUpdateWorkAnswersAHeldSlugAsDuplicate(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	h.seedWork(t, "Holder", func(value *work.Work) { value.Slug = stringPointer("held") })
	value := h.seedWork(t, "Mover")
	h.store.PutRoute("page-route")

	_, err := h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{ID: value.ID, Slug: stringPointer(" held ")})
	requireCode(t, err, connect.CodeAlreadyExists, "work with slug 'slug' already exists")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	_, err = h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{ID: value.ID, Slug: stringPointer("page-route")})
	requireCode(t, err, connect.CodeAlreadyExists, "work with slug 'page-route' already exists")

	cleared, err := h.service.UpdateWork(admin.context(t), work.UpdateWorkCommand{ID: value.ID, Slug: stringPointer("  ")})
	require.NoError(t, err)
	require.False(t, cleared.Changed, "clearing an absent slug changes nothing")
}

func clientIDs(clients []work.Client) []string {
	ids := make([]string, 0, len(clients))
	for _, client := range clients {
		ids = append(ids, client.ID)
	}
	return ids
}
