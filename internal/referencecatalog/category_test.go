package referencecatalog

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestCategoryServiceAdminMethodsRejectUnauthenticatedBeforeDatabase(t *testing.T) {
	t.Parallel()

	svc := &CategoryService{}
	ctx := context.Background()

	_, err := svc.ListCategoriesAdmin(ctx, connect.NewRequest(&managev1.ListCategoriesAdminRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = svc.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Unauthorized Category",
		Slug: new("unauthorized-category"),
	}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = svc.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{
		Id:   "category-id",
		Name: new("Unauthorized Category Update"),
	}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = svc.DeleteCategory(ctx, connect.NewRequest(&managev1.DeleteCategoryRequest{Id: "category-id"}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
