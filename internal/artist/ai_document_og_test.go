package artist_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/artist/artisttest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestExecuteAIDocumentMutationRequestsNoOGForADeletedTargetLocale(t *testing.T) {
	f := newRoomFixture(t)
	title := "한국어"
	f.h.store.PutLocale(f.artist.ID, artist.LocaleTitle{Locale: "ko", Title: &title, UpdatedAt: time.Now().UTC()})
	state, err := f.h.service.LoadAIDocumentState(f.contributor.context(t), f.artist.ID, "ko")
	require.NoError(t, err)
	require.True(t, state.LocaleExists)
	mutate := func(locale string, target *string, presence, create, remove bool) artist.AIDocumentMutationResult {
		t.Helper()
		result, err := f.h.service.ExecuteAIDocumentMutation(f.contributor.context(t), f.artist.ID, locale, artist.AIDocumentExecutionApply,
			func(state artist.AIDocumentState) (artist.AIDocumentMutation, error) {
				return artist.AIDocumentMutation{
					ArtistID: f.artist.ID, Locale: locale, ExpectedRevision: state.Revision,
					ExpectedTargetRevision: target, ExpectedSource: "en", ExpectedPresence: presence,
					ContributorMemberID: uuid.MustParse(f.contributor.memberID), CreateTranslation: create, DeleteTranslation: remove,
				}, nil
			})
		require.NoError(t, err)
		return result
	}

	deleted := mutate("ko", state.TargetRevision, true, false, true)

	require.True(t, deleted.Changed)
	_, exists := f.h.store.Locale(f.artist.ID, "ko")
	require.False(t, exists)
	require.Empty(t, f.h.store.OGRequests(), "a deleted target locale has no OG image to request")

	created := mutate("fr", nil, false, true, false)
	require.True(t, created.Changed)
	require.Equal(t, []artisttest.OGRequest{{ArtistID: f.artist.ID, Locale: "fr", Reason: "artist_ai_document_saved"}}, f.h.store.OGRequests())
}
