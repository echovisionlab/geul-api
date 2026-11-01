//go:build integration

package programevent_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// The use cases run on fakes in unit tests and every Store method keeps its
// contract on PostgreSQL in programeventpostgres. What stays here needs
// SpiceDB itself: the relationships a mutation writes, and an authority
// change that lands while a mutation waits for the event's root lock.

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Program Event integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Program Event integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// TestProgramEventAndSeriesMutationsSynchronouslyApplyAuthorizationIntegration
// checks that creating an event or a series grants its creator through
// SpiceDB before the request returns, and that deleting it revokes that.
func TestProgramEventAndSeriesMutationsSynchronouslyApplyAuthorizationIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := adminContext(t, db)
	service := newService(t, db, spiceDB)
	actor, err := policyv1.NewAccountIdentityActor(auth.GetUser(ctx).IdentityID.String())
	require.NoError(t, err)
	can := func(target policyv1.Can) bool {
		t.Helper()
		allowed, err := spiceDB.CheckActorCan(ctx, actor, target)
		require.NoError(t, err)
		return allowed
	}

	series := programevent.NewSeriesConnectHandler(service)
	created, err := series.CreateProgramEventSeries(ctx, connect.NewRequest(&managev1.CreateProgramEventSeriesRequest{
		Title: "Program Event sync series", Slug: "program-event-sync-series-" + uuid.NewString(),
	}))
	require.NoError(t, err)
	seriesCan, err := policyv1.ProgramEventSeries.Manage(created.Msg.Id)
	require.NoError(t, err)
	require.True(t, can(seriesCan))

	events := programevent.NewConnectHandler(service)
	event := createEvent(ctx, t, service)
	eventCan, err := policyv1.ProgramEvent.Manage(event)
	require.NoError(t, err)
	require.True(t, can(eventCan))

	_, err = events.DeleteProgramEvent(ctx, connect.NewRequest(&managev1.DeleteProgramEventRequest{Id: event}))
	require.NoError(t, err)
	require.False(t, can(eventCan))
	_, err = series.DeleteProgramEventSeries(ctx, connect.NewRequest(&managev1.DeleteProgramEventSeriesRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.False(t, can(seriesCan))
}

// TestProgramEventMutationsRecheckAuthorityAfterRootLockIntegration admits
// an Admin, holds the event's root lock until the update waits for it, and
// only then revokes the Admin's global role. The request check that passed
// is no authority for the write: the locked transaction must deny it.
func TestProgramEventMutationsRecheckAuthorityAfterRootLockIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := adminContext(t, db)
	service := newService(t, db, spiceDB)
	event := createEvent(ctx, t, service)

	lock := db.Begin()
	require.NoError(t, lock.Error)
	t.Cleanup(func() { _ = lock.Rollback().Error })
	var locked string
	require.NoError(t, lock.Raw(`SELECT id::text FROM program_event WHERE id = ? FOR UPDATE`, event).Scan(&locked).Error)
	require.Equal(t, event, locked)

	result := make(chan error, 1)
	go func() {
		timezone := "Asia/Seoul"
		_, err := programevent.NewConnectHandler(service).UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
			Id: event, Timezone: &timezone,
		}))
		result <- err
	}()
	requireLockWaiter(t, db, result)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, auth.GetUser(ctx).IdentityID.String(), policyv1.Role.User())
	require.NoError(t, lock.Commit().Error)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

	var timezone string
	require.NoError(t, db.Table("program_event").Select("timezone").Where("id = ?", event).Scan(&timezone).Error)
	require.Equal(t, "UTC", timezone)
}

// requireLockWaiter returns once a transaction of the test's database waits
// for a lock, and fails if the mutation returns first.
func requireLockWaiter(t *testing.T, db *gorm.DB, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-result:
			require.FailNow(t, "the mutation returned before the root lock was released", "error: %v", err)
		default:
		}
		var waiting int
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE NOT l.granted AND a.datname = current_database()`).Scan(&waiting).Error)
		if waiting > 0 {
			return
		}
		require.False(t, time.Now().After(deadline), "the mutation never waited for the root lock")
		time.Sleep(5 * time.Millisecond)
	}
}

// adminContext is a request of a new Admin, and the SpiceDB holding its role.
func adminContext(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Program Event integration Admin")
	spiceDB := testutil.SetupOryStack(t).SpiceDBClient
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.98")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}), spiceDB
}

// newService builds the Program Event use cases on PostgreSQL and SpiceDB,
// with a recording audit appender and stand-ins for signals and Block media
// delivery, which neither test reaches.
func newService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *programevent.Service {
	t.Helper()
	contentBlocks, err := contentblock.NewGeneratedStore(noFileReuse{})
	require.NoError(t, err)
	units, err := programeventpostgres.NewUnits(db, programeventpostgres.Ports{
		ContentBlocks: contentBlocks, Signals: acceptedSignals{}, Media: programeventadapter.NewRuntime(""),
		Files: noBlockMedia{}, Members: memberadapter.ContentMembers{}, CreditArtists: artistadapter.CreditArtists{},
		Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB),
	})
	require.NoError(t, err)
	service, err := programevent.NewService(programevent.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: &audittest.Recorder{},
		CreditMembers: programeventadapter.NewCreditMemberSummaries(db, ""),
	})
	require.NoError(t, err)
	return service
}

// createEvent stores a draft online event of a new type and returns its ID.
func createEvent(ctx context.Context, t *testing.T, service *programevent.Service) string {
	t.Helper()
	eventType, err := programevent.NewTypeConnectHandler(service).CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug: "program-event-type-" + uuid.NewString(), Locale: "en", Name: "Program Event type",
	}))
	require.NoError(t, err)
	event, err := programevent.NewConnectHandler(service).CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title: "Program Event", Slug: "program-event-" + uuid.NewString(), SourceLocale: "en", TypeId: eventType.Msg.Id,
		StartsAt: timestamppb.New(time.Now().UTC().Add(time.Hour)), Timezone: "UTC",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	return event.Msg.Id
}

type noFileReuse struct{}

func (noFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}

type acceptedSignals struct{}

func (acceptedSignals) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return nil
}

type noBlockMedia struct{}

func (noBlockMedia) HydrateAuthorizedProgramEventBlockMediaWithDB(
	_ context.Context, _ *gorm.DB, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}
