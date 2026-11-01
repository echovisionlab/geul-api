package page

import (
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPageTranslationApplyFenceAllowsDraftRootAndRejectsDeletedRoot(t *testing.T) {
	store := newMemoryStore()
	pageID := uuid.NewString()
	documentID := uuid.New()
	store.putPage(pageID, "en", "Source", documentID)

	fence := pageSystemTranslationDocumentFence(pageID)
	domain, err := fence(t.Context(), store, documentID)
	require.NoError(t, err)
	require.Equal(t, "en", domain.SourceLocale)

	// A deleted Page must reject a late result.
	delete(store.pages, pageID)
	_, err = fence(t.Context(), store, documentID)
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
}
