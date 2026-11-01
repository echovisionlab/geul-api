package programevent_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestAddProgramEventMediaCreatesUpdatesAndAuditsEachChange(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	gallery := uuid.NewString()

	added, err := h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{
		EventID: event.ID, FileID: gallery, Role: "gallery", Alt: text(" alt "),
	})
	require.NoError(t, err)
	require.True(t, added.Changed)
	require.True(t, added.Media.IsPrimary, "the first relation of a role is primary")
	require.Equal(t, "alt", *added.Media.Alt)
	require.Empty(t, bindings(h, "program_event", event.ID), "only posters own a public asset")

	unchanged, err := h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{
		EventID: event.ID, FileID: gallery, Role: "gallery", Alt: text("alt"),
	})
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, added.UpdatedAt, unchanged.UpdatedAt)

	cleared, err := h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{
		EventID: event.ID, FileID: gallery, Role: "gallery",
	})
	require.NoError(t, err)
	require.True(t, cleared.Changed)
	require.Nil(t, cleared.Media.Alt, "an omitted alt clears it")

	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range h.audit.Records() {
		operations = append(operations, record.ItemOperation)
	}
	require.Equal(t, []sharedtelemetry.AuditItemOperation{sharedtelemetry.AuditItemOperationCreated, sharedtelemetry.AuditItemOperationUpdated}, operations)

	_, err = h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{EventID: event.ID, FileID: gallery, Role: "cover"})
	requireCode(t, err, connect.CodeInvalidArgument, "role: unsupported program event media role")
}

func TestAddProgramEventPosterMovesThePrimaryAndBindsItsAsset(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	first, second := uuid.NewString(), uuid.NewString()
	_, err := h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{EventID: event.ID, FileID: first})
	require.NoError(t, err)
	_, err = h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{EventID: event.ID, FileID: second, MakePrimary: true})
	require.NoError(t, err)

	media := h.store.StoredMedia(event.ID)
	require.Len(t, media, 2)
	require.Equal(t, second, media[0].FileID)
	require.True(t, media[0].IsPrimary)
	require.False(t, media[1].IsPrimary)
	require.Len(t, bindings(h, "program_event", event.ID), 2)
	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, first, records[0].FileID)
	require.Equal(t, second, records[1].FileID)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, records[1].CollectionOperation)
}

func TestAddProgramEventMediaRollsBackWhenTheBindingFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	primary := uuid.NewString()
	_, err := h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{EventID: event.ID, FileID: primary})
	require.NoError(t, err)
	unready := errors.New("asset not ready")
	h.store.Fail = map[string]error{"BindPosterMedia": unready}

	_, err = h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{EventID: event.ID, FileID: uuid.NewString(), MakePrimary: true})

	require.ErrorIs(t, err, unready)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	media := h.store.StoredMedia(event.ID)
	require.Len(t, media, 1)
	require.Equal(t, primary, media[0].FileID)
	require.True(t, media[0].IsPrimary, "a rejected poster leaves the primary in place")
}

func TestDeleteProgramEventMediaPromotesTheNextOfItsRole(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	first, second := uuid.NewString(), uuid.NewString()
	for _, fileID := range []string{first, second} {
		_, err := h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{EventID: event.ID, FileID: fileID})
		require.NoError(t, err)
	}
	primary := h.store.StoredMedia(event.ID)[0]

	deleted, err := h.service.DeleteProgramEventMedia(newPrincipal().context(t), programevent.DeleteProgramEventMediaCommand{EventID: event.ID, MediaID: primary.ID})

	require.NoError(t, err)
	require.True(t, deleted.Changed)
	media := h.store.StoredMedia(event.ID)
	require.Len(t, media, 1)
	require.Equal(t, second, media[0].FileID)
	require.True(t, media[0].IsPrimary)
	require.NotContains(t, bindings(h, "program_event", event.ID), "media_"+primary.ID)
	last := h.audit.Records()[len(h.audit.Records())-1]
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, last.CollectionOperation, "the promoted poster is the change")
	require.Equal(t, second, last.FileID)

	_, err = h.service.DeleteProgramEventMedia(newPrincipal().context(t), programevent.DeleteProgramEventMediaCommand{EventID: event.ID, MediaID: primary.ID})
	requireCode(t, err, connect.CodeNotFound, "program event media not found: "+primary.ID)
}

func TestReorderProgramEventMediaOrdersTheWholeRole(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	for range 2 {
		_, err := h.service.AddProgramEventMedia(newPrincipal().context(t), programevent.AddProgramEventMediaCommand{EventID: event.ID, FileID: uuid.NewString(), Role: "gallery"})
		require.NoError(t, err)
	}
	media := h.store.StoredMedia(event.ID)
	reversed := []string{media[1].ID, media[0].ID}

	order, err := h.service.ReorderProgramEventMedia(newPrincipal().context(t), programevent.ReorderProgramEventMediaCommand{EventID: event.ID, Role: "gallery", MediaIDs: reversed})
	require.NoError(t, err)
	require.True(t, order.Changed)
	reordered := h.store.StoredMedia(event.ID)
	require.Equal(t, reversed[0], reordered[0].ID)
	require.True(t, reordered[0].IsPrimary)
	last := h.audit.Records()[len(h.audit.Records())-1]
	require.Equal(t, &reversed, last.ItemIDs)

	recorded := len(h.audit.Records())
	order, err = h.service.ReorderProgramEventMedia(newPrincipal().context(t), programevent.ReorderProgramEventMediaCommand{EventID: event.ID, Role: "gallery", MediaIDs: reversed})
	require.NoError(t, err)
	require.False(t, order.Changed)
	require.Len(t, h.audit.Records(), recorded)

	for message, ids := range map[string][]string{
		"media_ids: program event media order must include every item": {reversed[0]},
		"media_ids: duplicate program event media id":                  {reversed[0], reversed[0]},
		"media_ids: program event media id does not belong to event":   {reversed[0], uuid.NewString()},
	} {
		_, err := h.service.ReorderProgramEventMedia(newPrincipal().context(t), programevent.ReorderProgramEventMediaCommand{EventID: event.ID, Role: "gallery", MediaIDs: ids})
		requireCode(t, err, connect.CodeInvalidArgument, message)
	}
}

func TestProgramEventCreditsAddUpdateReorderAndDelete(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	member := uuid.NewString()
	h.store.PutMember(member)
	h.members.summaries[member] = &commonv1.MemberSummary{Id: member}

	first, err := h.service.AddProgramEventCredit(newPrincipal().context(t), programevent.AddProgramEventCreditCommand{EventID: event.ID, MemberID: &member, CreditRole: text(" host ")})
	require.NoError(t, err)
	require.Equal(t, "host", *first.CreditRole)
	require.Equal(t, h.members.summaries[member], first.Member)
	second, err := h.service.AddProgramEventCredit(newPrincipal().context(t), programevent.AddProgramEventCreditCommand{EventID: event.ID, DisplayName: text("Crew")})
	require.NoError(t, err)
	require.EqualValues(t, 1, second.SortOrder, "a credit without a position goes last")

	unchanged, err := h.service.UpdateProgramEventCredit(newPrincipal().context(t), programevent.UpdateProgramEventCreditCommand{EventID: event.ID, CreditID: first.ID, CreditRole: text("host")})
	require.NoError(t, err)
	require.Equal(t, first.ID, unchanged.ID)
	updated, err := h.service.UpdateProgramEventCredit(newPrincipal().context(t), programevent.UpdateProgramEventCreditCommand{EventID: event.ID, CreditID: first.ID, Description: text("Opening")})
	require.NoError(t, err)
	require.Equal(t, "Opening", *updated.Description)

	order, err := h.service.ReorderProgramEventCredits(newPrincipal().context(t), programevent.ReorderProgramEventCreditsCommand{EventID: event.ID, CreditIDs: []string{second.ID, first.ID}})
	require.NoError(t, err)
	require.True(t, order.Changed)
	require.Equal(t, second.ID, h.store.StoredCredits(event.ID)[0].ID)

	require.NoError(t, h.service.DeleteProgramEventCredit(newPrincipal().context(t), programevent.DeleteProgramEventCreditCommand{EventID: event.ID, CreditID: second.ID}))
	err = h.service.DeleteProgramEventCredit(newPrincipal().context(t), programevent.DeleteProgramEventCreditCommand{EventID: event.ID, CreditID: second.ID})
	requireCode(t, err, connect.CodeNotFound, "program event credit not found: "+second.ID)

	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range h.audit.Records() {
		operations = append(operations, record.ItemOperation)
	}
	require.Equal(t, []sharedtelemetry.AuditItemOperation{
		sharedtelemetry.AuditItemOperationCreated, sharedtelemetry.AuditItemOperationCreated,
		sharedtelemetry.AuditItemOperationUpdated, "", sharedtelemetry.AuditItemOperationDeleted,
	}, operations, "an unchanged credit is not audited; a reorder records the order")
}

func TestProgramEventCreditInputIsValidated(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	_, err := h.service.AddProgramEventCredit(newPrincipal().context(t), programevent.AddProgramEventCreditCommand{EventID: event.ID, DisplayName: text("A"), ArtistID: text(uuid.NewString())})
	requireCode(t, err, connect.CodeInvalidArgument, "credit: exactly one of artist_id, user_id, display_name is required")
	missing := uuid.NewString()
	_, err = h.service.AddProgramEventCredit(newPrincipal().context(t), programevent.AddProgramEventCreditCommand{EventID: event.ID, MemberID: &missing})
	requireCode(t, err, connect.CodeInvalidArgument, "member_id: not found: "+missing)
	_, err = h.service.AddProgramEventCredit(newPrincipal().context(t), programevent.AddProgramEventCreditCommand{DisplayName: text("A")})
	requireCode(t, err, connect.CodeInvalidArgument, "event_id is required")
	_, err = h.service.ReorderProgramEventCredits(newPrincipal().context(t), programevent.ReorderProgramEventCreditsCommand{EventID: event.ID, CreditIDs: []string{uuid.NewString()}})
	requireCode(t, err, connect.CodeInvalidArgument, "credit_ids: all credits must belong to the event")
	_, err = h.service.UpdateProgramEventCredit(newPrincipal().context(t), programevent.UpdateProgramEventCreditCommand{EventID: event.ID, CreditID: uuid.NewString(), CreditRole: text("x")})
	requireCode(t, err, connect.CodeNotFound, "")
}
