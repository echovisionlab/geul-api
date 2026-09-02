//go:build integration

package form_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestFormDeleteReleasesFeaturedImageBindingAndPreservesFileIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	principal := auth.GetUser(ctx)
	require.NotNil(t, principal)
	service := newFormServiceForIntegration(db, principal.IdentityID.String(), spiceDB)

	created, err := service.CreateForm(ctx, connect.NewRequest(&managev1.CreateFormRequest{
		Title:  "Form Image Cleanup " + integrationTestUUID(),
		Schema: integrationFormSchema(),
	}))
	require.NoError(t, err)
	fileID := seedFormImageBindingFile(t, db, "form/"+created.Msg.Id+"/featured.webp")
	_, err = service.SetFormFeaturedImage(ctx, connect.NewRequest(&managev1.SetFormFeaturedImageRequest{
		FormId: created.Msg.Id,
		FileId: fileID,
	}))
	require.NoError(t, err)
	var binding model.PublicAssetBinding
	require.NoError(t, db.Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?",
		"form", created.Msg.Id, "featured_image",
	).Take(&binding).Error)

	_, err = service.DeleteForm(ctx, connect.NewRequest(&managev1.DeleteFormRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	var bindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?",
		binding.OwnerType, binding.OwnerID, binding.BindingKey,
	).Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
	var asset model.PublicAsset
	require.NoError(t, db.Select("id", "status").Take(&asset, "id = ?", binding.AssetID).Error)
	require.Equal(t, model.PublicAssetStatusReady, asset.Status)
	var fileCount int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&fileCount).Error)
	require.Equal(t, int64(1), fileCount)
}
