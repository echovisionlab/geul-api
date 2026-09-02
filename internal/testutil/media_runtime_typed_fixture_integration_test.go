//go:build integration

package testutil

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestManagedEditorRuntimeFixturesUseTypedContentDocumentsIntegration(t *testing.T) {
	pg := SetupAppPostgres(t, AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true})
	fixtures := []ManagedEditorEntity{
		CreateManagedEditorEntity(t, pg.DB, nil, "", managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "en"),
		CreateManagedEditorEntity(t, pg.DB, nil, "", managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE, "en"),
		CreateManagedEditorEntity(t, pg.DB, nil, "", managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK, "en"),
	}
	for _, entity := range fixtures {
		fixture := SeedManagedEditorMediaFixture(t, pg.DB, entity, EditorMediaBlockTypeAudio)
		pending := SeedManagedEditorPendingUploadFixture(t, pg.DB, fixture, 1024)
		require.NotEmpty(t, pending.UploadID)

		var documentCount int64
		require.NoError(t, pg.DB.Raw(`
			SELECT COUNT(*)
			FROM content_document cd
			JOIN `+entity.Name+` owner ON owner.content_document_id = cd.id
			WHERE owner.id = ?
		`, entity.EntityID).Scan(&documentCount).Error)
		require.EqualValues(t, 1, documentCount)

		var fileReferenceCount int64
		require.NoError(t, pg.DB.Table("content_block_attachment").
			Where("block_id = ? AND reference_path = ?", fixture.BlockID, "file").
			Count(&fileReferenceCount).Error)
		require.EqualValues(t, 1, fileReferenceCount)
		require.Len(t, ReadManagedEditorRichTextBlocks(t, pg.DB, fixture), 1)
	}

	page := CreateManagedEditorEntity(t, pg.DB, nil, "", managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE, "en")
	immersive := SeedManagedImmersiveScenePageFixture(t, pg.DB, page)
	var immersiveReferences int64
	require.NoError(t, pg.DB.Table("content_block_attachment").
		Where("block_id = ?", immersive.SectionID).
		Count(&immersiveReferences).Error)
	require.EqualValues(t, 3, immersiveReferences)
	for _, referencePath := range immersive.SlotIDs() {
		var count int64
		require.NoError(t, pg.DB.Table("content_block_attachment").
			Where("block_id = ? AND reference_path = ?", immersive.SectionID, referencePath).
			Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
}
