//go:build integration

package page

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

var errPageMenuLifecycleFixture = errors.New("menu target lifecycle unavailable")

type failingPageMenuTargets struct{}

func (failingPageMenuTargets) UpdateSlug(
	context.Context,
	*gorm.DB,
	string,
	string,
	string,
	string,
) error {
	return errPageMenuLifecycleFixture
}

func (failingPageMenuTargets) Remove(
	context.Context,
	*gorm.DB,
	string,
	string,
	string,
) error {
	return errPageMenuLifecycleFixture
}

func TestPageMenuTargetFailureRollsBackSlugAndDeleteIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := pageIntegrationAdmin(t, db)
	pageSvc := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		newPageIntegrationFiles(db, spiceDB),
		noopAsyncPublisher{},
		nil,
		spiceDB,
		WithPageContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
		WithPageMenuTargets(failingPageMenuTargets{}),
	)

	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	originalSlug := "page-menu-rollback-original-" + suffix
	page, err := pageSvc.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Page menu rollback " + suffix,
		Slug:  &originalSlug,
	}))
	require.NoError(t, err)
	var contentDocumentID string
	require.NoError(t, db.Table("page").Select("content_document_id").
		Where("id = ?", page.Msg.Id).Scan(&contentDocumentID).Error)
	require.NotEmpty(t, contentDocumentID)

	nextSlug := "page-menu-rollback-next-" + suffix
	_, err = pageSvc.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{
		Id: page.Msg.Id, Slug: &nextSlug,
	}))
	require.ErrorIs(t, err, errPageMenuLifecycleFixture)
	var persisted model.Page
	require.NoError(t, db.Select("slug").First(&persisted, "id = ?", page.Msg.Id).Error)
	require.Equal(t, originalSlug, derefString(persisted.Slug))

	_, err = pageSvc.DeletePage(ctx, connect.NewRequest(&managev1.DeletePageRequest{Id: page.Msg.Id}))
	require.ErrorIs(t, err, errPageMenuLifecycleFixture)
	require.NoError(t, db.Select("slug").First(&persisted, "id = ?", page.Msg.Id).Error)
	require.Equal(t, originalSlug, derefString(persisted.Slug))
	var contentDocumentCount int64
	require.NoError(t, db.Table("content_document").Where("id = ?", contentDocumentID).
		Count(&contentDocumentCount).Error)
	require.Equal(t, int64(1), contentDocumentCount)
}
