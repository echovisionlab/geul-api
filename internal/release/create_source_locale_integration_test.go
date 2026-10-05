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

func TestCreateReleaseSourceLocaleSelectionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, adminID, "Release source locale Admin")
	ctx := releaseIntegrationAdminCtx(adminID)
	service := newReleaseIntegrationService(t, db, adminID, &recordingArtistFileDeleter{})
	store := newPageIntegrationContentBlockStore(t, integrationSpiceDB(t))
	require.NoError(t, db.Model(&model.Member{}).Where("id = ?", memberID).Update("preferred_locale", "ko").Error)
	for _, test := range []struct{ name, requested, preferred, header, want string }{
		{name: "explicit locale overrides member preference and header", requested: "en", preferred: "ko", header: "ja", want: "en"},
		{name: "omitted locale keeps member preference", preferred: "ko", header: "ja", want: "ko"},
		{name: "omitted locale falls back to header", header: "ja", want: "ja"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var preference any
			if test.preferred != "" {
				preference = test.preferred
			}
			require.NoError(t, db.Model(&model.Member{}).Where("id = ?", memberID).Update("preferred_locale", preference).Error)
			request := connect.NewRequest(&managev1.CreateReleaseRequest{Title: "Requested locale release", SourceLocale: test.requested, Type: managev1.ReleaseType_RELEASE_TYPE_ALBUM, Document: creativeContentIntegrationDocument(test.want, "Created in selected source locale")})
			request.Header().Set("Accept-Language", test.header)
			created, err := service.CreateRelease(ctx, request)
			require.NoError(t, err)
			var root model.Release
			require.NoError(t, db.First(&root, "id = ?", created.Msg.Id).Error)
			require.Equal(t, test.want, root.SourceLocale)
			require.NotNil(t, root.ContentDocumentID)
			documentID, err := uuid.Parse(*root.ContentDocumentID)
			require.NoError(t, err)
			// The domain root owns the source-locale pointer; content_document
			// stores the aggregate, with locales in content_block_locale.
			stored, err := store.LoadSnapshot(ctx, db, documentID, root.SourceLocale)
			require.NoError(t, err)
			storedDocument, err := contentblock.SnapshotToRichTextDocument(stored)
			require.NoError(t, err)
			require.Equal(t, test.want, storedDocument.SourceLocale)
			require.Len(t, storedDocument.LocaleOverlays, 1)
			require.Equal(t, test.want, storedDocument.LocaleOverlays[0].Locale)
			require.Equal(t, "Created in selected source locale", storedDocument.LocaleOverlays[0].Blocks[0].GetParagraph().Content[0].GetText().Text)
			var metadataLocales []string
			require.NoError(t, db.Table("release_translation").Where("entity_id = ?", root.ID).Pluck("locale", &metadataLocales).Error)
			require.Equal(t, []string{test.want}, metadataLocales)
			require.Equal(t, test.want, created.Msg.SourceLocale)
			require.Equal(t, test.want, created.Msg.Document.SourceLocale)
			require.Len(t, created.Msg.Document.LocaleOverlays, 1)
			require.Equal(t, test.want, created.Msg.Document.LocaleOverlays[0].Locale)
			require.Equal(t, "Created in selected source locale", created.Msg.Document.LocaleOverlays[0].Blocks[0].GetParagraph().Content[0].GetText().Text)
			read, err := service.GetRelease(ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: root.ID}))
			require.NoError(t, err)
			require.Equal(t, test.want, read.Msg.SourceLocale)
			require.Equal(t, test.want, read.Msg.Document.SourceLocale)
		})
	}
	before := int64(0)
	require.NoError(t, db.Model(&model.Release{}).Count(&before).Error)
	_, err := service.CreateRelease(ctx, connect.NewRequest(&managev1.CreateReleaseRequest{Title: "Invalid locale must not persist", SourceLocale: "unsupported", Type: managev1.ReleaseType_RELEASE_TYPE_ALBUM}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	after := int64(0)
	require.NoError(t, db.Model(&model.Release{}).Count(&after).Error)
	require.Equal(t, before, after)
}
