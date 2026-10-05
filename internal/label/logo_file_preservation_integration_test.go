//go:build integration

package label

import (
	"crypto/sha256"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLabelLogoReplacementAndRemovalPreserveFilesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Label Logo Admin")
	ctx := artistIntegrationAdminCtx(adminID)
	fileDeleter := &recordingArtistFileDeleter{}
	labelService := newLabelIntegrationService(t, db, adminID, fileDeleter)

	tests := []struct {
		name    string
		variant managev1.ThemeAssetVariant
	}{
		{name: "light", variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT},
		{name: "dark", variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labelID := createIntegrationLabel(
				t,
				labelService,
				ctx,
				"Label Logo "+tt.name+" "+integrationTestUUID(),
				"label-logo-"+tt.name+"-"+integrationTestUUID(),
			)
			originalFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/original-"+tt.name+".webp", "logo")
			replacementFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/replacement-"+tt.name+".webp", "logo")

			_, err := labelService.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
				LabelId: labelID,
				FileId:  originalFileID,
				Variant: tt.variant,
			}))
			require.NoError(t, err)

			_, err = labelService.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
				LabelId: labelID,
				FileId:  replacementFileID,
				Variant: tt.variant,
			}))
			require.NoError(t, err)
			require.Equal(t, replacementFileID, requireLabelLogoFileID(t, db, labelID, tt.variant))
			requireFileRowsPresent(t, db, originalFileID, replacementFileID)
			require.Empty(t, fileDeleter.deletedIDs)

			removed, err := labelService.DeleteLabelImage(ctx, connect.NewRequest(&managev1.DeleteLabelImageRequest{
				LabelId: labelID,
				Variant: tt.variant,
			}))
			require.NoError(t, err)
			require.True(t, removed.Msg.Success)
			require.Empty(t, requireLabelLogoFileID(t, db, labelID, tt.variant))
			requireFileRowsPresent(t, db, originalFileID, replacementFileID)
			require.Empty(t, fileDeleter.deletedIDs)
		})
	}

	t.Run("effective light-first fallback drives only meaningful OG changes", func(t *testing.T) {
		labelID := createIntegrationLabel(
			t,
			labelService,
			ctx,
			"Label Effective Logo "+integrationTestUUID(),
			"label-effective-logo-"+integrationTestUUID(),
		)
		lightFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/light.webp", "logo")
		darkFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/dark.webp", "logo")
		replacementDarkFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/dark-2.webp", "logo")

		darkSet, err := labelService.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
			LabelId: labelID, FileId: darkFileID,
			Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
		}))
		require.NoError(t, err)
		require.NotNil(t, darkSet.Msg.OgGenerationRunId)

		lightSet, err := labelService.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
			LabelId: labelID, FileId: lightFileID,
			Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
		}))
		require.NoError(t, err)
		require.NotNil(t, lightSet.Msg.OgGenerationRunId)

		darkReplacement, err := labelService.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
			LabelId: labelID, FileId: replacementDarkFileID,
			Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
		}))
		require.NoError(t, err)
		require.Nil(t, darkReplacement.Msg.OgGenerationRunId)

		lightRemoved, err := labelService.DeleteLabelImage(ctx, connect.NewRequest(&managev1.DeleteLabelImageRequest{
			LabelId: labelID,
			Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
		}))
		require.NoError(t, err)
		require.NotNil(t, lightRemoved.Msg.OgGenerationRunId)

		staleOgAssetID := seedReadyLabelOgAsset(t, db, "label-stale-og")
		require.NoError(t, mediaasset.NewLifecycle(db, "").BindPublicAsset(ctx, mediaasset.Binding{
			AssetID: staleOgAssetID, OwnerType: "label", OwnerID: labelID, BindingKey: "og",
		}))
		require.NoError(t, db.Model(&model.Label{}).Where("id = ?", labelID).
			Update("og_asset_id", staleOgAssetID).Error)

		darkRemoved, err := labelService.DeleteLabelImage(ctx, connect.NewRequest(&managev1.DeleteLabelImageRequest{
			LabelId: labelID,
			Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
		}))
		require.NoError(t, err)
		require.Nil(t, darkRemoved.Msg.OgGenerationRunId)
		requireFileRowsPresent(t, db, lightFileID, darkFileID, replacementDarkFileID)

		var activeTargets int64
		require.NoError(t, db.Model(&model.OgGenerationTarget{}).
			Where("entity_type = ? AND entity_id = ? AND latest_generation_id IS NOT NULL", "label", labelID).
			Count(&activeTargets).Error)
		require.Zero(t, activeTargets)
		var label model.Label
		require.NoError(t, db.Select("id", "og_asset_id").First(&label, "id = ?", labelID).Error)
		require.Nil(t, label.OgAssetID)
		requireNoPublicAssetBinding(t, db, "label", labelID, "og")
		requirePublicAssetDeletePending(t, db, staleOgAssetID)
	})

	t.Run("global reconciliation clears stale no-logo Label OG state", func(t *testing.T) {
		labelID := createIntegrationLabel(
			t,
			labelService,
			ctx,
			"Label Global Reconcile "+integrationTestUUID(),
			"label-global-reconcile-"+integrationTestUUID(),
		)
		darkFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/global-dark.webp", "logo")
		_, err := labelService.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
			LabelId: labelID, FileId: darkFileID,
			Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
		}))
		require.NoError(t, err)
		var target model.OgGenerationTarget
		require.NoError(t, db.Where("entity_type = ? AND entity_id = ?", "label", labelID).Take(&target).Error)
		require.NotNil(t, target.LatestGenerationID)
		queuedGenerationID := *target.LatestGenerationID
		require.NoError(t, db.Model(&model.Label{}).Where("id = ?", labelID).
			Update("logo_dark_file_id", nil).Error)
		require.NoError(t, mediaasset.NewLifecycle(db, "").ReleaseExactPublicAssetBindings(
			ctx, "label", labelID, []string{"logo:dark"},
		))

		staleOgAssetID := seedReadyLabelOgAsset(t, db, "label-global-stale-og")
		require.NoError(t, mediaasset.NewLifecycle(db, "").BindPublicAsset(ctx, mediaasset.Binding{
			AssetID: staleOgAssetID, OwnerType: "label", OwnerID: labelID, BindingKey: "og",
		}))
		require.NoError(t, db.Model(&model.Label{}).Where("id = ?", labelID).
			Update("og_asset_id", staleOgAssetID).Error)

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return labeladapter.NewGlobalReconciler().ReconcileBeforeGlobalGeneration(ctx, tx, "")
		}))

		var label model.Label
		require.NoError(t, db.Select("id", "og_asset_id").First(&label, "id = ?", labelID).Error)
		require.Nil(t, label.OgAssetID)
		requireNoPublicAssetBinding(t, db, "label", labelID, "og")
		requirePublicAssetDeletePending(t, db, staleOgAssetID)
		require.NoError(t, db.First(&target, "id = ?", target.ID).Error)
		require.Nil(t, target.LatestGenerationID)
		var generation model.OgGeneration
		require.NoError(t, db.First(&generation, "id = ?", queuedGenerationID).Error)
		require.Equal(t, model.OgGenerationStatusCancelled, generation.Status)
	})

	t.Run("Label deletion releases logo and OG bindings but preserves Files", func(t *testing.T) {
		labelID := createIntegrationLabel(
			t,
			labelService,
			ctx,
			"Label Delete Assets "+integrationTestUUID(),
			"label-delete-assets-"+integrationTestUUID(),
		)
		lightFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/delete-light.webp", "logo")
		darkFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/delete-dark.webp", "logo")
		for _, input := range []struct {
			fileID  string
			variant managev1.ThemeAssetVariant
		}{
			{lightFileID, managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT},
			{darkFileID, managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK},
		} {
			_, err := labelService.SetLabelImage(ctx, connect.NewRequest(&managev1.SetLabelImageRequest{
				LabelId: labelID, FileId: input.fileID, Variant: input.variant,
			}))
			require.NoError(t, err)
		}

		preview, err := labelService.PreviewDeleteLabel(ctx, connect.NewRequest(&managev1.PreviewDeleteLabelRequest{Id: labelID}))
		require.NoError(t, err)
		_, err = labelService.DeleteLabel(ctx, connect.NewRequest(&managev1.DeleteLabelRequest{
			Id: labelID, PreviewRevision: preview.Msg.Revision,
		}))
		require.NoError(t, err)
		requireFileRowsPresent(t, db, lightFileID, darkFileID)

		var remainingBindings int64
		require.NoError(t, db.Model(&model.PublicAssetBinding{}).
			Where("owner_type = ? AND owner_id = ?", "label", labelID).
			Count(&remainingBindings).Error)
		require.Zero(t, remainingBindings)
	})
}

func requireLabelLogoFileID(
	t *testing.T,
	db *gorm.DB,
	labelID string,
	variant managev1.ThemeAssetVariant,
) string {
	t.Helper()

	column, err := labelLogoColumnForVariant(variant)
	require.NoError(t, err)
	var fileID *string
	require.NoError(t, db.Table("label").
		Select(column).
		Where("id = ?", labelID).
		Row().
		Scan(&fileID))
	if fileID == nil {
		return ""
	}
	return *fileID
}

func seedReadyLabelOgAsset(t *testing.T, db *gorm.DB, content string) string {
	t.Helper()
	assetLifecycle := mediaasset.NewLifecycle(db, "")
	asset, _, err := assetLifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		Kind:        "og",
		Extension:   "webp",
		MimeType:    "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(content))
	_, err = assetLifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: int64(len(content)), Sha256: digest[:],
	})
	require.NoError(t, err)
	return asset.ID
}

func requireFileRowsPresent(t *testing.T, db *gorm.DB, fileIDs ...string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Model(&model.File{}).Where("id IN ?", fileIDs).Count(&count).Error)
	require.Equal(t, int64(len(fileIDs)), count)
}
