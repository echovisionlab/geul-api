//go:build integration

package release_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCreateReleaseExplicitSourceLocaleOverridesPreferencesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, adminID, "Release source locale Admin")
	ctx := releaseIntegrationAdminCtx(adminID)
	service := newReleaseIntegrationService(t, db, adminID, &recordingArtistFileDeleter{})
	store := newPageIntegrationContentBlockStore(t, integrationSpiceDB(t))
	require.NoError(t, db.Model(&model.Member{}).Where("id = ?", memberID).Update("preferred_locale", "ko").Error)
	request := connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: "Requested locale release", SourceLocale: "en", Type: managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Created in selected source locale"),
	})
	request.Header().Set("Accept-Language", "ja")
	created, err := service.CreateRelease(ctx, request)
	require.NoError(t, err)
	var root model.Release
	require.NoError(t, db.First(&root, "id = ?", created.Msg.Id).Error)
	require.Equal(t, "en", root.SourceLocale)
	require.NotNil(t, root.ContentDocumentID)
	documentID, err := uuid.Parse(*root.ContentDocumentID)
	require.NoError(t, err)
	stored, err := store.LoadSnapshot(ctx, db, documentID, root.SourceLocale)
	require.NoError(t, err)
	storedDocument, err := contentblock.SnapshotToRichTextDocument(stored)
	require.NoError(t, err)
	require.Equal(t, "en", storedDocument.SourceLocale)
	require.Len(t, storedDocument.LocaleOverlays, 1)
	require.Equal(t, "en", storedDocument.LocaleOverlays[0].Locale)
	require.Equal(t, "Created in selected source locale", storedDocument.LocaleOverlays[0].Blocks[0].GetParagraph().Content[0].GetText().Text)
	var metadataLocales []string
	require.NoError(t, db.Table("release_translation").Where("entity_id = ?", root.ID).Pluck("locale", &metadataLocales).Error)
	require.Equal(t, []string{"en"}, metadataLocales)
	require.Equal(t, "en", created.Msg.SourceLocale)
	require.Equal(t, "en", created.Msg.Document.SourceLocale)
	read, err := service.GetRelease(ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: root.ID}))
	require.NoError(t, err)
	require.Equal(t, "en", read.Msg.SourceLocale)
	require.Equal(t, "en", read.Msg.Document.SourceLocale)
}
