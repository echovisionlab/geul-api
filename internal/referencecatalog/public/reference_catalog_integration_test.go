//go:build integration

package public

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPublicReferenceCatalogReadIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	suffix := testutil.IntegrationUUID()[:8]
	categoryOne := seedPublicReferenceCategory(t, db, "Public Ref Category A "+suffix, "public-ref-category-a-"+suffix)
	categoryTwo := seedPublicReferenceCategory(t, db, "Public Ref Category B "+suffix, "public-ref-category-b-"+suffix)
	tagOne := seedPublicReferenceTag(t, db, "Public Ref Tag A "+suffix, "public-ref-tag-a-"+suffix)
	tagTwo := seedPublicReferenceTag(t, db, "Public Ref Tag B "+suffix, "public-ref-tag-b-"+suffix)

	seedPublicReferencePost(t, db, "POST_STATUS_DRAFT", categoryOne, tagOne)
	seedPublicReferencePost(t, db, "POST_STATUS_PUBLISHED", categoryOne, tagOne)
	seedPublicReferencePost(t, db, "POST_STATUS_ARCHIVED", categoryOne, tagOne)
	seedPublicReferencePost(t, db, "POST_STATUS_PUBLISHED", categoryTwo, tagTwo)

	t.Run("categories expose public post counts search sort and pagination", func(t *testing.T) {
		response, err := NewCategoryService(db).List(context.Background(), connect.NewRequest(&openv1.ListCategoriesRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "Public Ref Category",
			}},
			Sorts: []*commonv1.SortSpec{{
				Field: "post_count", Order: commonv1.SortOrder_SORT_ORDER_DESC,
			}},
			Pagination: &commonv1.PaginationRequest{Limit: 1},
		}))
		require.NoError(t, err)
		require.Len(t, response.Msg.Categories, 1)
		require.Equal(t, categoryOne, response.Msg.Categories[0].Id)
		require.EqualValues(t, 2, response.Msg.Categories[0].PostCount)
		require.EqualValues(t, 2, response.Msg.Pagination.Total)
		require.EqualValues(t, 1, response.Msg.Pagination.Limit)
		require.True(t, response.Msg.Pagination.HasMore)

		_, err = NewCategoryService(db).List(context.Background(), connect.NewRequest(&openv1.ListCategoriesRequest{
			Filters: []*commonv1.FilterSpec{{Field: "unknown", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "x"}},
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		_, err = NewCategoryService(db).List(context.Background(), connect.NewRequest(&openv1.ListCategoriesRequest{
			Sorts: []*commonv1.SortSpec{{Field: "unknown", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("tags expose public post counts search sort and pagination", func(t *testing.T) {
		response, err := NewTagService(db).List(context.Background(), connect.NewRequest(&openv1.ListTagsRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "Public Ref Tag",
			}},
			Sorts: []*commonv1.SortSpec{{
				Field: "post_count", Order: commonv1.SortOrder_SORT_ORDER_DESC,
			}},
			Pagination: &commonv1.PaginationRequest{Limit: 1},
		}))
		require.NoError(t, err)
		require.Len(t, response.Msg.Tags, 1)
		require.Equal(t, tagOne, response.Msg.Tags[0].Id)
		require.EqualValues(t, 2, response.Msg.Tags[0].PostCount)
		require.EqualValues(t, 2, response.Msg.Pagination.Total)
		require.EqualValues(t, 1, response.Msg.Pagination.Limit)
		require.True(t, response.Msg.Pagination.HasMore)

		_, err = NewTagService(db).List(context.Background(), connect.NewRequest(&openv1.ListTagsRequest{
			Filters: []*commonv1.FilterSpec{{Field: "unknown", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "x"}},
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		_, err = NewTagService(db).List(context.Background(), connect.NewRequest(&openv1.ListTagsRequest{
			Sorts: []*commonv1.SortSpec{{Field: "unknown", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
}

func seedPublicReferenceCategory(t *testing.T, db *gorm.DB, name string, slug string) string {
	t.Helper()
	category := model.Category{ID: testutil.IntegrationUUID(), Name: name, Slug: slug}
	require.NoError(t, db.Create(&category).Error)
	return category.ID
}

func seedPublicReferenceTag(t *testing.T, db *gorm.DB, name string, slug string) string {
	t.Helper()
	tag := model.Tag{ID: testutil.IntegrationUUID(), Name: name, Slug: slug}
	require.NoError(t, db.Create(&tag).Error)
	return tag.ID
}

func seedPublicReferencePost(
	t *testing.T,
	db *gorm.DB,
	status string,
	categoryID string,
	tagID string,
) {
	t.Helper()
	documentID := testutil.IntegrationUUID()
	postID := testutil.IntegrationUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'post')`, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, status, content_document_id) VALUES (?::uuid, ?, ?::uuid)`,
		postID,
		status,
		documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post_category (post_id, category_id) VALUES (?::uuid, ?::uuid)`, postID, categoryID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post_tag (post_id, tag_id) VALUES (?::uuid, ?::uuid)`, postID, tagID,
	).Error)
}
