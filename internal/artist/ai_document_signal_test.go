package artist

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtistAIDocumentContentUpdateCoversTranslationLifecycle(t *testing.T) {
	source := artistAIDocumentContentUpdate(
		AIDocumentMutation{ArtistID: "artist", Locale: "en", ExpectedSource: "en"},
		AIDocumentMutationResult{Revision: "revision", Changed: true},
		"member",
	)
	require.Equal(t, ContentUpdate{ArtistID: "artist", Kind: LocaleUpdate, Locale: LocaleChange{
		Fields: []string{"content"}, DocumentRevision: "revision", Contributors: []string{"member"},
		Source: SourceAI, Locale: "en", DocumentStateChanged: true,
	}}, source)

	targetRevision := "target-revision"
	target := artistAIDocumentContentUpdate(
		AIDocumentMutation{ArtistID: "artist", Locale: "ko", ExpectedSource: "en"},
		AIDocumentMutationResult{Revision: "revision", TargetRevision: &targetRevision, Changed: true},
		"member",
	)
	require.Equal(t, "ko", target.Locale.Locale)
	require.False(t, target.Locale.LocaleDeleted)
	require.Equal(t, &targetRevision, target.Locale.TargetRevision)
	require.False(t, target.Locale.DocumentStateChanged)

	deleted := artistAIDocumentContentUpdate(
		AIDocumentMutation{ArtistID: "artist", Locale: "ko", ExpectedSource: "en", DeleteTranslation: true},
		AIDocumentMutationResult{Revision: "revision", Changed: true},
		"member",
	)
	require.Equal(t, "ko", deleted.Locale.Locale)
	require.True(t, deleted.Locale.LocaleDeleted)
	require.Nil(t, deleted.Locale.TargetRevision)
	require.False(t, deleted.Locale.DocumentStateChanged)
}
