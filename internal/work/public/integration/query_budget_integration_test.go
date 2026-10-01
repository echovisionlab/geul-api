//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWorkPublicListFeaturedImageQueryBudgetIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	stack, err := sharedPublicIntegrationStack()
	require.NoError(t, err)
	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Name: "Work List Query Budget Admin"})
	memberID := seedPublicAdminMemberIdentityLink(t, db, identityID, "Work List Query Budget Admin")
	adminCtx := publicLegalAdminCtx(memberID, identityID)
	management := newPublicWorkManageService(t, db, identityID)
	const fixtureSize = 20
	for i := 0; i < fixtureSize; i++ {
		created, createErr := management.CreateWork(adminCtx, connect.NewRequest(&managev1.CreateWorkRequest{
			Title: fmt.Sprintf("Work query budget fixture %02d", i),
			Type:  managev1.WorkType_WORK_TYPE_PORTFOLIO, Year: 2026, Month: 9,
			IsPresent: boolPtr(true), Document: emptyPublicWorkDocument("en"),
		}))
		require.NoError(t, createErr)
		_, createErr = management.PublishWork(adminCtx, connect.NewRequest(&managev1.PublishWorkRequest{Id: created.Msg.Id}))
		require.NoError(t, createErr)
	}

	publicDB, err := gorm.Open(postgres.Open(stack.Postgres.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := publicDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	service := workpublic.NewWorkService(
		publicDB,
		publicIntegrationSpiceDB,
		extractedWorkPublicMediaHydrator{},
		newPublicWorkRuntimeForTest(publicDB, "https://cdn.example.com"),
		workadapter.NewMemberSummaries(publicDB, "https://cdn.example.com"),
		referencecatalogadapter.PublicMapPlaces{},
		workpublic.WithWorkContentBlockStore(newPublicWorkContentBlockStore(t)),
	)
	recorder := &workListQueryRecorder{}
	require.NoError(t, publicDB.Callback().Query().After("gorm:query").Register("record_work_list_featured_queries", recorder.record))
	require.NoError(t, publicDB.Callback().Row().After("gorm:row").Register("record_work_list_featured_rows", recorder.record))

	for _, pageSize := range []int32{1, 6, fixtureSize} {
		recorder.reset()
		response, listErr := service.List(context.Background(), connect.NewRequest(&openv1.ListWorksRequest{
			Pagination: &commonv1.PaginationRequest{Limit: pageSize},
		}))
		require.NoError(t, listErr)
		require.Len(t, response.Msg.Works, int(pageSize))
		totalQueries, featuredFileQueries := recorder.counts()
		t.Logf("fixture_size=%d page_size=%d select_queries=%d redundant_featured_file_source_queries=%d", fixtureSize, pageSize, totalQueries, featuredFileQueries)
		require.Equal(t, 7, totalQueries, "fixed SELECT budget for a page without featured images")
		require.Zero(t, featuredFileQueries, "Work.featured_image_file_id must come from the loaded page row")
	}
}

type workListQueryRecorder struct {
	mu                  sync.Mutex
	selectQueries       int
	featuredFileQueries int
}

func (r *workListQueryRecorder) record(tx *gorm.DB) {
	sql := strings.ToLower(strings.TrimSpace(tx.Statement.SQL.String()))
	if !strings.HasPrefix(sql, "select") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selectQueries++
	if tx.Statement.Table == "work" &&
		strings.Contains(sql, "featured_image_file_id as file_id") &&
		strings.Contains(sql, "where work.id =") {
		r.featuredFileQueries++
	}
}

func (r *workListQueryRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selectQueries = 0
	r.featuredFileQueries = 0
}

func (r *workListQueryRecorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.selectQueries, r.featuredFileQueries
}
