//go:build integration

package referencecatalog

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestReferenceCatalogCRUDIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	spiceDB := stack.SpiceDBClient
	referenceAdmin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	referenceAdminID := referenceAdmin.IdentityID
	ctx := auth.WithUser(t.Context(), referenceAdmin.AuthUserInfo())

	t.Run("category", func(t *testing.T) {
		db := testutil.NewIntegrationDB(t)
		svc := NewCategoryService(db, referenceMenuTargetsForTest(nil), spiceDB)
		description := "News and announcements"
		suffix := referenceShortSuffix()
		name := "Integration News " + suffix
		slug := "integration-news-" + suffix

		created, err := svc.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
			Name:        name,
			Slug:        &slug,
			Description: &description,
		}))
		require.NoError(t, err)
		require.NotEmpty(t, created.Msg.Id)
		require.Equal(t, slug, created.Msg.GetSlug())
		require.Equal(t, description, created.Msg.GetDescription())
		requireResourceManageAccess(t, spiceDB, policyv1.Category.Manage, created.Msg.Id, referenceAdminID, true)

		listed, err := svc.ListCategories(ctx, connect.NewRequest(&managev1.ListCategoriesRequest{
			Filters: referenceSearchFilters(name),
			Sorts:   referenceNameAscSort(),
			Pagination: &commonv1.PaginationRequest{
				Limit: 10,
			},
		}))
		require.NoError(t, err)
		require.Equal(t, int32(1), listed.Msg.GetPagination().GetTotal())
		require.Len(t, listed.Msg.Categories, 1)
		require.Equal(t, created.Msg.Id, listed.Msg.Categories[0].Id)

		removePostMapping := seedReferencePostMapping(t, db, "post_category", "category_id", created.Msg.Id)

		adminListed, err := svc.ListCategoriesAdmin(ctx, connect.NewRequest(&managev1.ListCategoriesAdminRequest{
			Filters: referenceSearchFilters(name),
			Sorts:   referenceNameAscSort(),
			Pagination: &commonv1.PaginationRequest{
				Limit: 10,
			},
		}))
		require.NoError(t, err)
		require.Equal(t, int32(1), adminListed.Msg.GetPagination().GetTotal())
		require.Len(t, adminListed.Msg.Categories, 1)
		require.Equal(t, created.Msg.Id, adminListed.Msg.Categories[0].Category.Id)
		require.Equal(t, int32(1), adminListed.Msg.Categories[0].PostCount)

		removePostMapping()

		updatedName := "Integration News Updated"
		updatedSlug := "integration-news-updated-" + suffix
		updatedDescription := "Updated announcements"
		updated, err := svc.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{
			Id:          created.Msg.Id,
			Name:        &updatedName,
			Slug:        &updatedSlug,
			Description: &updatedDescription,
		}))
		require.NoError(t, err)
		require.Equal(t, updatedName, updated.Msg.Name)
		require.Equal(t, updatedSlug, updated.Msg.GetSlug())
		require.Equal(t, updatedDescription, updated.Msg.GetDescription())

		deleted, err := svc.DeleteCategory(ctx, connect.NewRequest(&managev1.DeleteCategoryRequest{Id: created.Msg.Id}))
		require.NoError(t, err)
		require.True(t, deleted.Msg.Success)
		requireResourceManageAccess(t, spiceDB, policyv1.Category.Manage, created.Msg.Id, referenceAdminID, false)
		requireNoRow(t, db, "category", created.Msg.Id)
	})

	t.Run("category API boundaries reject invalid input and missing resources", func(t *testing.T) {
		db := testutil.NewIntegrationDB(t)
		svc := NewCategoryService(db, referenceMenuTargetsForTest(nil), spiceDB)
		suffix := referenceShortSuffix()
		missingID := testutil.IntegrationUUID()

		_, err := svc.ListCategories(ctx, connect.NewRequest(&managev1.ListCategoriesRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "id",
				Op:    commonv1.FilterOp_FILTER_OP_EQ,
				Value: missingID,
			}},
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		_, err = svc.ListCategories(ctx, connect.NewRequest(&managev1.ListCategoriesRequest{
			Sorts: []*commonv1.SortSpec{{
				Field: "unknown",
				Order: commonv1.SortOrder_SORT_ORDER_ASC,
			}},
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		_, err = svc.ListCategoriesAdmin(ctx, connect.NewRequest(&managev1.ListCategoriesAdminRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "id",
				Op:    commonv1.FilterOp_FILTER_OP_EQ,
				Value: missingID,
			}},
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		_, err = svc.ListCategoriesAdmin(ctx, connect.NewRequest(&managev1.ListCategoriesAdminRequest{
			Sorts: []*commonv1.SortSpec{{
				Field: "unknown",
				Order: commonv1.SortOrder_SORT_ORDER_ASC,
			}},
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		emptyAdminList, err := svc.ListCategoriesAdmin(ctx, connect.NewRequest(&managev1.ListCategoriesAdminRequest{
			Filters: referenceSearchFilters("category-no-match-" + suffix),
			Pagination: &commonv1.PaginationRequest{
				Limit: 10,
			},
		}))
		require.NoError(t, err)
		require.Empty(t, emptyAdminList.Msg.GetCategories())
		require.Equal(t, int32(0), emptyAdminList.Msg.GetPagination().GetTotal())

		primarySlug := "category-boundary-primary-" + suffix
		primary, err := svc.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
			Name: "Category Boundary Primary " + suffix,
			Slug: &primarySlug,
		}))
		require.NoError(t, err)

		_, err = svc.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
			Name: "Category Boundary Duplicate " + suffix,
			Slug: &primarySlug,
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

		secondarySlug := "category-boundary-secondary-" + suffix
		secondary, err := svc.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
			Name: "Category Boundary Secondary " + suffix,
			Slug: &secondarySlug,
		}))
		require.NoError(t, err)

		_, err = svc.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{
			Id:   missingID,
			Name: strPtr("Missing Category Update " + suffix),
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

		_, err = svc.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{
			Id:   secondary.Msg.GetId(),
			Slug: &primarySlug,
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

		_, err = svc.DeleteCategory(context.Background(), connect.NewRequest(&managev1.DeleteCategoryRequest{
			Id: primary.Msg.GetId(),
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

		_, err = svc.DeleteCategory(ctx, connect.NewRequest(&managev1.DeleteCategoryRequest{Id: missingID}))
		require.Error(t, err)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("tag", func(t *testing.T) {
		db := testutil.NewIntegrationDB(t)
		svc := NewTagService(db, referenceMenuTargetsForTest(nil), spiceDB)
		suffix := referenceShortSuffix()
		name := "Integration Tag " + suffix
		slug := "integration-tag-" + suffix

		created, err := svc.CreateTag(ctx, connect.NewRequest(&managev1.CreateTagRequest{
			Name: name,
			Slug: &slug,
		}))
		require.NoError(t, err)
		require.NotEmpty(t, created.Msg.Id)
		require.Equal(t, slug, created.Msg.GetSlug())
		requireResourceManageAccess(t, spiceDB, policyv1.Tag.Manage, created.Msg.Id, referenceAdminID, true)

		listed, err := svc.ListTags(ctx, connect.NewRequest(&managev1.ListTagsRequest{
			Filters: referenceSearchFilters(name),
			Sorts:   referenceNameAscSort(),
			Pagination: &commonv1.PaginationRequest{
				Limit: 10,
			},
		}))
		require.NoError(t, err)
		require.Equal(t, int32(1), listed.Msg.GetPagination().GetTotal())
		require.Len(t, listed.Msg.Tags, 1)
		require.Equal(t, created.Msg.Id, listed.Msg.Tags[0].Id)

		removePostMapping := seedReferencePostMapping(t, db, "post_tag", "tag_id", created.Msg.Id)

		adminListed, err := svc.ListTagsAdmin(ctx, connect.NewRequest(&managev1.ListTagsAdminRequest{
			Filters: referenceSearchFilters(name),
			Sorts:   referenceNameAscSort(),
			Pagination: &commonv1.PaginationRequest{
				Limit: 10,
			},
		}))
		require.NoError(t, err)
		require.Equal(t, int32(1), adminListed.Msg.GetPagination().GetTotal())
		require.Len(t, adminListed.Msg.Tags, 1)
		require.Equal(t, created.Msg.Id, adminListed.Msg.Tags[0].Tag.Id)
		require.Equal(t, int32(1), adminListed.Msg.Tags[0].PostCount)

		removePostMapping()

		updatedName := "Integration Tag Updated"
		updatedSlug := "integration-tag-updated-" + suffix
		updated, err := svc.UpdateTag(ctx, connect.NewRequest(&managev1.UpdateTagRequest{
			Id:   created.Msg.Id,
			Name: &updatedName,
			Slug: &updatedSlug,
		}))
		require.NoError(t, err)
		require.Equal(t, updatedName, updated.Msg.Name)
		require.Equal(t, updatedSlug, updated.Msg.GetSlug())

		deleted, err := svc.DeleteTag(ctx, connect.NewRequest(&managev1.DeleteTagRequest{Id: created.Msg.Id}))
		require.NoError(t, err)
		require.True(t, deleted.Msg.Success)
		requireResourceManageAccess(t, spiceDB, policyv1.Tag.Manage, created.Msg.Id, referenceAdminID, false)
		requireNoRow(t, db, "tag", created.Msg.Id)
	})

	t.Run("client", func(t *testing.T) {
		db := testutil.NewIntegrationDB(t)
		svc := NewClientService(db, referenceCatalogTestAssets{}, spiceDB)
		website := "https://client.example.com"
		suffix := referenceShortSuffix()
		name := "Integration Client " + suffix

		created, err := svc.CreateClient(ctx, connect.NewRequest(&managev1.CreateClientRequest{
			Name:    name,
			Website: &website,
		}))
		require.NoError(t, err)
		require.NotEmpty(t, created.Msg.Id)
		require.Equal(t, website, created.Msg.GetWebsite())
		requireResourceManageAccess(t, spiceDB, policyv1.Client.Manage, created.Msg.Id, referenceAdminID, true)

		fetched, err := svc.GetClient(ctx, connect.NewRequest(&managev1.GetClientRequest{Id: created.Msg.Id}))
		require.NoError(t, err)
		require.Equal(t, created.Msg.Id, fetched.Msg.Id)

		listed, err := svc.ListClients(ctx, connect.NewRequest(&managev1.ListClientsRequest{
			Filters: referenceSearchFilters(name),
			Sorts:   referenceNameAscSort(),
			Pagination: &commonv1.PaginationRequest{
				Limit: 10,
			},
		}))
		require.NoError(t, err)
		require.Equal(t, int32(1), listed.Msg.GetPagination().GetTotal())
		require.Len(t, listed.Msg.Clients, 1)
		require.Equal(t, created.Msg.Id, listed.Msg.Clients[0].Id)

		search, err := svc.SearchClients(ctx, connect.NewRequest(&managev1.SearchClientsRequest{
			Query: suffix,
			Limit: 10,
		}))
		require.NoError(t, err)
		require.Len(t, search.Msg.Clients, 1)
		require.Equal(t, created.Msg.Id, search.Msg.Clients[0].Id)

		adminListed, err := svc.ListClientsAdmin(ctx, connect.NewRequest(&managev1.ListClientsAdminRequest{
			Filters: referenceSearchFilters(name),
			Sorts:   referenceNameAscSort(),
			Pagination: &commonv1.PaginationRequest{
				Limit: 10,
			},
		}))
		require.NoError(t, err)
		require.Equal(t, int32(1), adminListed.Msg.GetPagination().GetTotal())
		require.Len(t, adminListed.Msg.Clients, 1)
		require.Equal(t, created.Msg.Id, adminListed.Msg.Clients[0].Client.Id)
		require.Zero(t, adminListed.Msg.Clients[0].WorkCount)

		updatedName := "Integration Client Updated"
		updatedWebsite := "https://client-updated.example.com"
		updated, err := svc.UpdateClient(ctx, connect.NewRequest(&managev1.UpdateClientRequest{
			Id:      created.Msg.Id,
			Name:    &updatedName,
			Website: &updatedWebsite,
		}))
		require.NoError(t, err)
		require.Equal(t, updatedName, updated.Msg.Name)
		require.Equal(t, updatedWebsite, updated.Msg.GetWebsite())

		deleted, err := svc.DeleteClient(ctx, connect.NewRequest(&managev1.DeleteClientRequest{Id: created.Msg.Id}))
		require.NoError(t, err)
		require.True(t, deleted.Msg.Success)
		requireResourceManageAccess(t, spiceDB, policyv1.Client.Manage, created.Msg.Id, referenceAdminID, false)
		requireNoRow(t, db, "client", created.Msg.Id)
	})
}

func TestReferenceCategoryMutationsRecheckAdminAfterIdentityLockIntegration(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *CategoryService, context.Context, string) string
		invoke  func(context.Context, *CategoryService, string, string) error
		assert  func(*testing.T, *gorm.DB, string, string)
	}{
		{
			name: "create",
			prepare: func(_ *testing.T, _ *CategoryService, _ context.Context, _ string) string {
				return ""
			},
			invoke: func(ctx context.Context, service *CategoryService, _ string, suffix string) error {
				_, err := service.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
					Name: "Reference race category " + suffix,
					Slug: strPtr("reference-race-category-" + suffix),
				}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, _ string, suffix string) {
				var count int64
				require.NoError(t, db.Table("category").Where("slug = ?", "reference-race-category-"+suffix).Count(&count).Error)
				require.Zero(t, count)
			},
		},
		{
			name: "update",
			prepare: func(t *testing.T, service *CategoryService, ctx context.Context, suffix string) string {
				created, err := service.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
					Name: "Reference race category original " + suffix,
					Slug: strPtr("reference-race-category-original-" + suffix),
				}))
				require.NoError(t, err)
				return created.Msg.Id
			},
			invoke: func(ctx context.Context, service *CategoryService, id, suffix string) error {
				name := "Reference race category updated " + suffix
				_, err := service.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{Id: id, Name: &name}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, id, suffix string) {
				var name string
				require.NoError(t, db.Table("category").Select("name").Where("id = ?", id).Scan(&name).Error)
				require.Equal(t, "Reference race category original "+suffix, name)
			},
		},
		{
			name: "delete",
			prepare: func(t *testing.T, service *CategoryService, ctx context.Context, suffix string) string {
				created, err := service.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
					Name: "Reference race category delete " + suffix,
					Slug: strPtr("reference-race-category-delete-" + suffix),
				}))
				require.NoError(t, err)
				return created.Msg.Id
			},
			invoke: func(ctx context.Context, service *CategoryService, id, _ string) error {
				_, err := service.DeleteCategory(ctx, connect.NewRequest(&managev1.DeleteCategoryRequest{Id: id}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, id, _ string) {
				var count int64
				require.NoError(t, db.Table("category").Where("id = ?", id).Count(&count).Error)
				require.Equal(t, int64(1), count)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.PrepareOryIntegrationConcurrentTest(t).DB
			stack := testutil.PrepareOryIntegrationConcurrentTest(t)
			admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
			identityID := admin.IdentityID
			spiceDB := stack.SpiceDBClient
			service := NewCategoryService(db, referenceMenuTargetsForTest(nil), spiceDB)
			ctx := auth.WithUser(t.Context(), admin.AuthUserInfo())
			suffix := referenceShortSuffix()
			id := test.prepare(t, service, ctx, suffix)

			roleChangeTx := db.Begin()
			require.NoError(t, roleChangeTx.Error)
			require.NoError(t, roleChangeTx.Exec("SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", identityID).Error)

			result := make(chan error, 1)
			go func() {
				result <- test.invoke(ctx, service, id, suffix)
			}()
			requireReferenceMutationStillWaiting(t, result)
			testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.User())
			require.NoError(t, roleChangeTx.Commit().Error)

			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
			test.assert(t, db, id, suffix)
		})
	}
}

func requireReferenceMutationStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "reference mutation returned before identity state was released", "error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}

func requireResourceManageAccess(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	manage func(string) (policyv1.Can, error),
	resourceID string,
	identityID string,
	expected bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := manage(resourceID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, expected, allowed)
}

func strPtr(value string) *string {
	return &value
}

func referenceShortSuffix() string {
	return testutil.IntegrationUUID()[:8]
}

func referenceSearchFilters(value string) []*commonv1.FilterSpec {
	return []*commonv1.FilterSpec{{
		Field: "search",
		Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
		Value: value,
	}}
}

func referenceNameAscSort() []*commonv1.SortSpec {
	return []*commonv1.SortSpec{{
		Field: "name",
		Order: commonv1.SortOrder_SORT_ORDER_ASC,
	}}
}
