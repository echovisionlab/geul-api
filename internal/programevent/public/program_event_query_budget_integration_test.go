//go:build integration

package public

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type publicProgramEventQueryCounter struct {
	logger.Interface
	count *atomic.Int64
}

func (l publicProgramEventQueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func TestPublicProgramEventListQueryBudgetDoesNotGrowPerRowIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	fixtures := seedPublicProgramEventListQueryFixtures(t, db, 20)

	var queryCount atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: publicProgramEventQueryCounter{
		Interface: db.Config.Logger,
		count:     &queryCount,
	}})
	service := NewProgramEventService(
		countedDB,
		newPublicProgramEventAssets(countedDB, "https://cdn.example.com"),
		&publicProgramEventQueryBudgetCredits{},
	)
	eventCounts := make(map[int]int64, 3)
	for _, limit := range []int32{1, 6, 20} {
		queryCount.Store(0)
		request := connect.NewRequest(&openv1.ListProgramEventsRequest{
			Pagination: &commonv1.PaginationRequest{Limit: limit},
		})
		request.Header().Set("Accept-Language", "fr-CA")
		response, err := service.List(context.Background(), request)
		require.NoError(t, err)
		require.Len(t, response.Msg.Events, int(limit))
		eventCounts[int(limit)] = queryCount.Load()

		for index, event := range response.Msg.Events {
			fixture := fixtures[index]
			require.Equal(t, fixture.eventID, event.Id)
			require.Equal(t, fixture.expectedSummary, event.GetSummary())
			require.Equal(t, fixture.expectedDisplayedLocale, event.GetLocalizationInfo().GetDisplayedLocale())
			require.Equal(t, fixture.expectedTypeName, event.GetType().GetName())
			if fixture.posterAssetID == "" {
				require.Nil(t, event.PosterAsset)
			} else {
				require.Equal(t,
					"https://cdn.example.com/asset/"+fixture.posterAssetID+"/poster.webp",
					event.GetPosterAsset().GetUrl(),
				)
			}
		}
	}
	t.Logf("event list query counts for 1/6/20 rows: %v", eventCounts)
	require.Equal(t, eventCounts[1], eventCounts[6])
	require.Equal(t, eventCounts[1], eventCounts[20])
	require.LessOrEqual(t, eventCounts[20], int64(11))
}

func TestPublicProgramEventTypeListQueryBudgetDoesNotGrowPerRowIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	seedPublicProgramEventListQueryFixtures(t, db, 20)

	var queryCount atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: publicProgramEventQueryCounter{
		Interface: db.Config.Logger,
		count:     &queryCount,
	}})
	service := NewProgramEventTypeService(countedDB)
	typeCounts := make(map[int]int64, 3)
	for _, limit := range []int32{1, 6, 20} {
		queryCount.Store(0)
		request := connect.NewRequest(&openv1.ListProgramEventTypesRequest{
			Pagination: &commonv1.PaginationRequest{Limit: limit},
		})
		request.Header().Set("Accept-Language", "fr-CA")
		response, err := service.List(context.Background(), request)
		require.NoError(t, err)
		require.Len(t, response.Msg.Types, int(limit))
		typeCounts[int(limit)] = queryCount.Load()
		for index, eventType := range response.Msg.Types {
			require.Equal(t, fmt.Sprintf("query-budget-type-%02d-fr", index), eventType.Name)
		}
	}
	t.Logf("event type list query counts for 1/6/20 rows: %v", typeCounts)
	require.Equal(t, typeCounts[1], typeCounts[6])
	require.Equal(t, typeCounts[1], typeCounts[20])
	require.LessOrEqual(t, typeCounts[20], int64(3))
}

type publicProgramEventQueryFixture struct {
	eventID                 string
	expectedSummary         string
	expectedDisplayedLocale string
	expectedTypeName        string
	posterAssetID           string
}

type publicProgramEventQueryBudgetCredits struct{}

func (*publicProgramEventQueryBudgetCredits) LoadPublicCreditMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error) {
	return map[string]*commonv1.MemberSummary{}, nil
}

func seedPublicProgramEventListQueryFixtures(t *testing.T, db *gorm.DB, count int) []publicProgramEventQueryFixture {
	t.Helper()
	suffix := uuid.NewString()
	fixtures := make([]publicProgramEventQueryFixture, 0, count)
	base := time.Now().UTC().Add(time.Hour)
	for index := 0; index < count; index++ {
		typeID := uuid.NewString()
		typeSlug := fmt.Sprintf("query-budget-type-%s-%02d", suffix, index)
		require.NoError(t, db.Create(&model.ProgramEventType{
			ID: typeID, Slug: typeSlug,
			Status:    openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String(),
			SortOrder: int32(index), CreatedAt: base, UpdatedAt: base,
		}).Error)
		for _, locale := range []string{"de", "en", "fr"} {
			require.NoError(t, db.Create(&model.ProgramEventTypeLocale{
				TypeID: typeID,
				Locale: locale,
				Name:   fmt.Sprintf("query-budget-type-%02d-%s", index, locale),
			}).Error)
		}

		eventID := uuid.NewString()
		documentID := uuid.NewString()
		sourceLocale := "de"
		if index%2 == 0 {
			sourceLocale = "en"
		}
		title := fmt.Sprintf("Query Budget Event %02d", index)
		require.NoError(t, db.Exec(
			"INSERT INTO content_document (id, profile) VALUES (?::uuid, 'program_event')",
			documentID,
		).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO program_event (
				id, content_document_id, title, slug, status, source_locale, type_id,
				starts_at, timezone, location_mode, created_at, updated_at
			) VALUES (?::uuid, ?::uuid, ?, ?, ?, ?, ?::uuid, ?, 'UTC', ?, ?, ?)
		`, eventID, documentID, title, fmt.Sprintf("query-budget-event-%s-%02d", suffix, index),
			managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(), sourceLocale,
			typeID, base.Add(time.Duration(index)*time.Minute),
			managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA.String(), base, base).Error)
		require.NoError(t, db.Create(&model.ProgramEventTranslation{
			EntityID: eventID, Locale: sourceLocale,
			Summary: stringPtr(fmt.Sprintf("Summary %02d", index)), CreatedAt: base, UpdatedAt: base,
		}).Error)
		expectedLocale := sourceLocale
		expectedSummary := fmt.Sprintf("Summary %02d", index)
		if index%2 == 0 {
			expectedLocale = "fr"
			expectedSummary = fmt.Sprintf("Résumé %02d", index)
			require.NoError(t, db.Create(&model.ProgramEventTranslation{
				EntityID: eventID, Locale: "fr",
				Summary: stringPtr(fmt.Sprintf("Résumé %02d", index)), CreatedAt: base, UpdatedAt: base,
			}).Error)
		}

		posterAssetID := ""
		if index != count-1 {
			fileID, assetID := seedCanonicalPublicFileFixture(
				t, db, fmt.Sprintf("query-budget-poster-%02d.webp", index), "image/webp", "poster",
			)
			posterAssetID = assetID
			require.NoError(t, db.Exec(`
				INSERT INTO program_event_media (
					id, event_id, file_id, role, sort_order, is_primary, created_at, updated_at
				) VALUES (?::uuid, ?::uuid, ?::uuid, 'poster', 0, true, ?, ?)
			`, uuid.NewString(), eventID, fileID, base, base).Error)
			if index == 0 {
				unselectedFileID, _ := seedCanonicalPublicFileFixture(
					t, db, "query-budget-unselected.webp", "image/webp", "poster",
				)
				require.NoError(t, db.Exec(`
					INSERT INTO program_event_media (
						id, event_id, file_id, role, sort_order, is_primary, created_at, updated_at
					) VALUES (?::uuid, ?::uuid, ?::uuid, 'poster', -1, false, ?, ?)
				`, uuid.NewString(), eventID, unselectedFileID, base, base).Error)
			}
		}
		fixtures = append(fixtures, publicProgramEventQueryFixture{
			eventID: eventID, expectedSummary: expectedSummary, expectedDisplayedLocale: expectedLocale,
			expectedTypeName: fmt.Sprintf("query-budget-type-%02d-%s", index, expectedLocale), posterAssetID: posterAssetID,
		})
	}
	return fixtures
}
