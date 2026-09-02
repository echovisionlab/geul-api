//go:build integration

package programevent

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestProgramEventTypeAdminEditAndDeleteBoundaryIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Program Event Type Admin")
	ctx := programEventIntegrationAdminCtx(adminID)
	suffix := integrationTestUUID()
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	service := NewProgramEventTypeService(db, spiceDB)
	contentBlocks := newProgramEventIntegrationContentBlockStore(t, spiceDB)

	created, err := service.CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug:   "event-type-" + suffix,
		Locale: "en",
		Name:   "Event Type " + suffix,
	}))
	require.NoError(t, err)

	inactive := managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE
	updatedSlug := "updated-event-type-" + suffix
	updatedName := "Updated Event Type " + suffix
	updated, err := service.UpdateProgramEventType(ctx, connect.NewRequest(&managev1.UpdateProgramEventTypeRequest{
		Id:     created.Msg.Id,
		Slug:   &updatedSlug,
		Status: &inactive,
		Locale: "ko",
		Name:   &updatedName,
	}))
	require.NoError(t, err)
	require.Equal(t, updatedSlug, updated.Msg.Slug)
	require.Equal(t, inactive, updated.Msg.Status)
	require.Contains(t, updated.Msg.Locales, &managev1.ProgramEventTypeLocale{Locale: "ko", Name: updatedName})

	_, err = service.UpdateProgramEventType(ctx, connect.NewRequest(&managev1.UpdateProgramEventTypeRequest{
		Id:   created.Msg.Id,
		Slug: stringPtr("invalid/nested"),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	eventService := NewProgramEventService(db, newProgramEventRuntime("https://cdn.example.com"), spiceDB, newProgramEventCreditMemberSummaries(db, "https://cdn.example.com"))
	eventService.contentBlocks = contentBlocks
	_, err = eventService.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Uses Event Type " + suffix,
		Slug:         "uses-event-type-" + suffix,
		SourceLocale: "en",
		TypeId:       created.Msg.Id,
		StartsAt:     timestamppb.New(time.Now().UTC().Add(time.Hour)),
		Timezone:     "UTC",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)

	_, err = service.DeleteProgramEventType(ctx, connect.NewRequest(&managev1.DeleteProgramEventTypeRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	unused, err := service.CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug:   "unused-event-type-" + suffix,
		Locale: "en",
		Name:   "Unused Event Type " + suffix,
	}))
	require.NoError(t, err)
	deleted, err := service.DeleteProgramEventType(ctx, connect.NewRequest(&managev1.DeleteProgramEventTypeRequest{Id: unused.Msg.Id}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
}
