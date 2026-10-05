//go:build integration

package programevent

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProgramEventCreditOnlyUpdateReportsPersistedChangesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Credit update result admin")
	ctx := programEventAuditedMemberContext(t, identityID, memberID)
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	writer := apitelemetry.NewDurableWriter(db)
	types := NewAuditedProgramEventTypeService(db, writer, spiceDB)
	eventType, err := types.CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug: "credit-result-type-" + integrationTestUUID(), Locale: "en", Name: "Credit result type",
	}))
	require.NoError(t, err)
	service := NewAuditedProgramEventService(
		db, newProgramEventRuntime(""), newProgramEventIntegrationFileService(db, spiceDB), writer,
		spiceDB, newProgramEventCreditMemberSummaries(db, ""),
		WithProgramEventContentBlockStore(newProgramEventIntegrationContentBlockStore(t, spiceDB)),
	)
	event, err := service.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title: "Credit result", Slug: "credit-result-" + integrationTestUUID(), SourceLocale: "en",
		TypeId: eventType.Msg.Id, StartsAt: timestamppb.New(time.Now().UTC().Add(time.Hour)),
		Timezone: "UTC", LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	readEvent := func(t *testing.T) model.ProgramEvent {
		t.Helper()
		var row model.ProgramEvent
		require.NoError(t, db.First(&row, "id = ?", event.Msg.Id).Error)
		return row
	}
	update := func(t *testing.T, credits []*managev1.ProgramEventCredit) []*managev1.ProgramEventCredit {
		t.Helper()
		before := readEvent(t)
		auditBefore := programEventAuditCount(t, db, event.Msg.Id)
		response, err := service.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
			Id: event.Msg.Id, ReplaceCredits: true, Credits: credits,
		}))
		require.NoError(t, err)
		require.True(t, response.Msg.Changed)
		after := readEvent(t)
		require.True(t, after.UpdatedAt.After(before.UpdatedAt), "credit-only writes advance the event timestamp")
		require.WithinDuration(t, after.UpdatedAt, response.Msg.UpdatedAt.AsTime(), time.Microsecond)
		require.Greater(t, programEventAuditCount(t, db, event.Msg.Id), auditBefore)
		loaded, err := service.loadProgramEvent(ctx, event.Msg.Id)
		require.NoError(t, err)

		// Reapplying the persisted shape keeps both timestamp and audit count unchanged.
		auditAfter := programEventAuditCount(t, db, event.Msg.Id)
		noop, err := service.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
			Id: event.Msg.Id, ReplaceCredits: true, Credits: loaded.Credits,
		}))
		require.NoError(t, err)
		require.False(t, noop.Msg.Changed)
		require.True(t, after.UpdatedAt.Equal(noop.Msg.UpdatedAt.AsTime()))
		require.True(t, after.UpdatedAt.Equal(readEvent(t).UpdatedAt))
		require.Equal(t, auditAfter, programEventAuditCount(t, db, event.Msg.Id))
		return loaded.Credits
	}
	var credits []*managev1.ProgramEventCredit
	t.Run("add", func(t *testing.T) {
		credits = update(t, []*managev1.ProgramEventCredit{
			{DisplayName: ptrString("First credit"), SortOrder: 0},
			{DisplayName: ptrString("Second credit"), SortOrder: 1},
		})
		require.Len(t, credits, 2)
		require.Equal(t, "First credit", credits[0].GetDisplayName())
		require.Equal(t, "Second credit", credits[1].GetDisplayName())
	})
	t.Run("edit", func(t *testing.T) {
		require.Len(t, credits, 2)
		credits[0].CreditRole = ptrString("host")
		credits = update(t, credits)
		require.Equal(t, "host", credits[0].GetCreditRole())
	})
	t.Run("order", func(t *testing.T) {
		require.Len(t, credits, 2)
		firstID, secondID := credits[0].Id, credits[1].Id
		credits[0].SortOrder, credits[1].SortOrder = 1, 0
		credits = update(t, []*managev1.ProgramEventCredit{credits[1], credits[0]})
		require.Equal(t, []string{secondID, firstID}, []string{credits[0].Id, credits[1].Id})
	})
	t.Run("delete all", func(t *testing.T) {
		require.Len(t, credits, 2)
		credits = update(t, []*managev1.ProgramEventCredit{})
		require.Empty(t, credits)
	})
}
