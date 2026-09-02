//go:build integration

package programevent

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProgramEventAndSeriesMutationsSynchronouslyApplyAuthorizationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	contentBlocks := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	require.NoError(t, err)

	typeResponse, err := NewProgramEventTypeService(db, spiceDB).CreateProgramEventType(
		ctx,
		connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
			Slug: "program-event-sync-type-" + integrationTestUUID(), Locale: "en", Name: "Program Event sync type",
		}),
	)
	require.NoError(t, err)

	seriesService := NewProgramEventSeriesService(db, newProgramEventRuntime(""), spiceDB)
	seriesResponse, err := seriesService.CreateProgramEventSeries(ctx, connect.NewRequest(&managev1.CreateProgramEventSeriesRequest{
		Title: "Program Event sync series", Slug: "program-event-sync-series-" + integrationTestUUID(),
	}))
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	seriesCan, err := policyv1.ProgramEventSeries.Manage(seriesResponse.Msg.Id)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(ctx, actor, seriesCan)
	require.NoError(t, err)
	require.True(t, allowed)

	eventService := NewProgramEventService(db, newProgramEventRuntime(""), spiceDB, newProgramEventCreditMemberSummaries(db, ""))
	eventService.contentBlocks = contentBlocks
	eventResponse, err := eventService.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Program Event sync event",
		Slug:         "program-event-sync-event-" + integrationTestUUID(),
		SourceLocale: "en",
		TypeId:       typeResponse.Msg.Id,
		StartsAt:     timestamppb.New(time.Now().UTC().Add(time.Hour)),
		Timezone:     "UTC",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	eventCan, err := policyv1.ProgramEvent.Manage(eventResponse.Msg.Id)
	require.NoError(t, err)
	allowed, err = spiceDB.CheckActorCan(ctx, actor, eventCan)
	require.NoError(t, err)
	require.True(t, allowed)

	_, err = eventService.DeleteProgramEvent(ctx, connect.NewRequest(&managev1.DeleteProgramEventRequest{Id: eventResponse.Msg.Id}))
	require.NoError(t, err)
	allowed, err = spiceDB.CheckActorCan(ctx, actor, eventCan)
	require.NoError(t, err)
	require.False(t, allowed)

	_, err = seriesService.DeleteProgramEventSeries(ctx, connect.NewRequest(&managev1.DeleteProgramEventSeriesRequest{Id: seriesResponse.Msg.Id}))
	require.NoError(t, err)
	allowed, err = spiceDB.CheckActorCan(ctx, actor, seriesCan)
	require.NoError(t, err)
	require.False(t, allowed)
}
