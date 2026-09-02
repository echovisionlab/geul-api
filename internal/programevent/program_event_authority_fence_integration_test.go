//go:build integration

package programevent

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Each case admits an Admin, waits at the Program Event root lock, and only
// then revokes its global role. A successful initial request check is not
// authority for the later write: the locked transaction must deny it.
func TestProgramEventMutationsRecheckAuthorityAfterRootLockIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	suffix := integrationTestUUID()
	store := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	files := newProgramEventIntegrationFileService(db, spiceDB)

	typeResponse, err := NewProgramEventTypeService(db, spiceDB).CreateProgramEventType(
		ctx,
		connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
			Slug:              "program-event-fence-type-" + suffix,
			Locale:            "en",
			Name:              "Program Event fence type",
			RequiresPlace:     ptrBool(false),
			RequiresStreamUrl: ptrBool(false),
		}),
	)
	require.NoError(t, err)

	eventService := NewProgramEventService(db, newProgramEventRuntime(""), spiceDB, newProgramEventCreditMemberSummaries(db, ""))
	eventService.contentBlocks = store
	seriesService := NewProgramEventSeriesService(db, newProgramEventRuntime(""), spiceDB)
	series, err := seriesService.CreateProgramEventSeries(ctx, connect.NewRequest(&managev1.CreateProgramEventSeriesRequest{
		Title: "Program Event Series authority fence",
		Slug:  "program-event-series-fence-" + suffix,
	}))
	require.NoError(t, err)
	event, err := eventService.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Program Event authority fence",
		Slug:         "program-event-fence-" + suffix,
		SourceLocale: "en",
		TypeId:       typeResponse.Msg.Id,
		StartsAt:     timestamppb.New(time.Now().UTC().Add(time.Hour)),
		Timezone:     "UTC",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)

	t.Run("metadata", func(t *testing.T) {
		lockTx := lockAdminMutationRoot(t, db, "program_event", "id = '"+event.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		go func() {
			updatedTimezone := "Asia/Seoul"
			_, err := eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
				Id: event.Msg.Id, Timezone: &updatedTimezone,
			}))
			result <- err
		}()
		requireAdminMutationWaiting(t, result)
		demoteAdminMutationActor(t, spiceDB, ctx)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

		var timezone string
		require.NoError(t, db.Table("program_event").Select("timezone").Where("id = ?", event.Msg.Id).Scan(&timezone).Error)
		require.Equal(t, "UTC", timezone)
	})

	// Restore the one actor for independent checks. The previous request did
	// not mutate the Event, and only the direct global role is changing here.
	grantIntegrationGlobalRole(t, spiceDB, auth.GetUser(ctx).IdentityID.String(), policyv1.Role.Admin())

	t.Run("series", func(t *testing.T) {
		lockTx := lockAdminMutationRoot(t, db, "program_event_series", "id = '"+series.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		go func() {
			updatedTitle := "must not persist"
			_, err := seriesService.UpdateProgramEventSeries(ctx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{
				Id: series.Msg.Id, Title: &updatedTitle,
			}))
			result <- err
		}()
		requireAdminMutationWaiting(t, result)
		demoteAdminMutationActor(t, spiceDB, ctx)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

		var title string
		require.NoError(t, db.Table("program_event_series").Select("title").Where("id = ?", series.Msg.Id).Scan(&title).Error)
		require.Equal(t, "Program Event Series authority fence", title)
	})

	grantIntegrationGlobalRole(t, spiceDB, auth.GetUser(ctx).IdentityID.String(), policyv1.Role.Admin())

	t.Run("collaboration bootstrap", func(t *testing.T) {
		sessionID := insertProgramEventIntegrationSession(t, db, auth.GetUser(ctx).IdentityID.String())
		lockTx := lockAdminMutationRoot(t, db, "program_event", "id = '"+event.Msg.Id+"'::uuid")
		internalService := NewAuditedInternalProgramEventService(
			db,
			&capturingAsyncPublisher{},
			apitelemetry.NewDurableWriter(db),
			WithInternalProgramEventSpiceDB(spiceDB),
			WithInternalProgramEventCheckpoints(programEventIntegrationCheckpoints(db, spiceDB)),
			WithInternalProgramEventContentBlockStore(store),
			WithInternalProgramEventMediaHydrator(files),
		)
		result := make(chan error, 1)
		go func() {
			_, err := internalService.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
				EventId:   event.Msg.Id,
				Locale:    "en",
				Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
			}))
			result <- err
		}()
		requireAdminMutationWaiting(t, result)
		demoteAdminMutationActor(t, spiceDB, ctx)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

		var auditCount int64
		require.NoError(t, db.Table("domain_audit").Where("target_id = ? AND attributes @> ?::jsonb", event.Msg.Id, `{"changed_fields":["locale_content"]}`).Count(&auditCount).Error)
		require.Zero(t, auditCount)
	})
}
