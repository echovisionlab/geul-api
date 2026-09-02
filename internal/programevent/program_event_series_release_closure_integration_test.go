//go:build integration

package programevent

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestProgramEventSeriesAdminListCapsRequestedPageSizeIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	suffix := uuid.NewString()
	for index := 0; index < 105; index++ {
		require.NoError(t, db.Exec(`
			INSERT INTO program_event_series (
				id, slug, status, title, created_at, updated_at
			) VALUES (?::uuid, ?, ?, ?, NOW(), NOW())
		`, uuid.NewString(), suffix+"-"+uuid.NewString(),
			managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(), "Series").Error)
	}

	response, err := NewProgramEventSeriesService(db, newProgramEventRuntime(""), spiceDB).ListProgramEventSeriesAdmin(
		ctx,
		connect.NewRequest(&managev1.ListProgramEventSeriesAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: 1000},
		}),
	)
	require.NoError(t, err)
	require.Len(t, response.Msg.Series, 100)
	require.Equal(t, int32(100), response.Msg.GetPagination().GetLimit())
	require.True(t, response.Msg.GetPagination().GetHasMore())
}

func TestProgramEventSeriesAdminGetAcceptsCanonicalSlugIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	id := uuid.NewString()
	slug := "canonical-event-series-" + uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO program_event_series (
			id, slug, status, title, created_at, updated_at
		) VALUES (?::uuid, ?, ?, ?, NOW(), NOW())
	`, id, slug, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(), "Canonical Series").Error)
	policy, err := policyv1.ProgramEventSeries.TouchPolicy(id)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)

	response, err := NewProgramEventSeriesService(db, newProgramEventRuntime(""), spiceDB).GetProgramEventSeries(
		ctx,
		connect.NewRequest(&managev1.GetProgramEventSeriesRequest{Id: slug}),
	)
	require.NoError(t, err)
	require.Equal(t, id, response.Msg.Id)
}

func TestDeleteProgramEventSeriesDetachesEventsWithoutDeletingThemIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	seriesID := uuid.NewString()
	eventTypeID := uuid.NewString()
	eventID := uuid.NewString()
	documentID := seedServiceIntegrationContentDocument(t, db, "program_event")
	require.NoError(t, db.Exec(`
		INSERT INTO program_event_series (
			id, slug, status, title, created_at, updated_at
		) VALUES (?::uuid, ?, ?, 'Series to delete', NOW(), NOW())
	`, seriesID, "series-delete-"+seriesID,
		managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO program_event_type (
			id, slug, status, created_at, updated_at
		) VALUES (?::uuid, ?, 'PROGRAM_EVENT_TYPE_STATUS_ACTIVE', NOW(), NOW())
	`, eventTypeID, "event-type-"+eventTypeID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO program_event (
			id, title, slug, status, source_locale, type_id, series_id, series_order,
			starts_at, timezone, location_mode, content_document_id, created_at, updated_at
		) VALUES (
			?::uuid, 'Preserved Event', ?, 'PROGRAM_EVENT_STATUS_DRAFT', 'en', ?::uuid,
			?::uuid, 4, NOW(), 'UTC', 'PROGRAM_EVENT_LOCATION_MODE_ONLINE', ?::uuid, NOW(), NOW()
		)
	`, eventID, "event-"+eventID, eventTypeID, seriesID, documentID).Error)
	policy, err := policyv1.ProgramEventSeries.TouchPolicy(seriesID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)

	_, err = NewProgramEventSeriesService(db, newProgramEventRuntime(""), spiceDB).DeleteProgramEventSeries(
		ctx,
		connect.NewRequest(&managev1.DeleteProgramEventSeriesRequest{Id: seriesID}),
	)
	require.NoError(t, err)

	var event struct {
		SeriesID    *string `gorm:"column:series_id"`
		SeriesOrder *int32  `gorm:"column:series_order"`
	}
	require.NoError(t, db.Table("program_event").Select("series_id, series_order").Where("id = ?", eventID).Take(&event).Error)
	require.Nil(t, event.SeriesID)
	require.Nil(t, event.SeriesOrder)
}
