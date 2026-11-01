package work

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func TestWorkTranslationApplyFenceAllowsNonDeletedLifecycleStates(t *testing.T) {
	for _, status := range []Status{StatusDraft, StatusArchived} {
		t.Run(string(status), func(t *testing.T) {
			workID := uuid.NewString()
			documentID := uuid.New()
			store := stubStore{
				lockDocumentRoot: func(id uuid.UUID) (Root, error) {
					require.Equal(t, documentID, id)
					return Root{ID: workID, Status: status}, nil
				},
				sourceLocale: func(string) (string, error) { return "en", nil },
			}

			domain, err := workSystemTranslationDocumentFence(workID)(t.Context(), store, documentID)
			require.NoError(t, err)
			require.Equal(t, "en", domain.SourceLocale)
		})
	}
}

func TestWorkTranslationApplyFenceRejectsDeletedOrMovedRoot(t *testing.T) {
	workID := uuid.NewString()
	store := stubStore{lockDocumentRoot: func(uuid.UUID) (Root, error) { return Root{}, ErrWorkNotFound }}

	_, err := workSystemTranslationDocumentFence(workID)(t.Context(), store, uuid.New())
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))

	store.lockDocumentRoot = func(uuid.UUID) (Root, error) { return Root{ID: uuid.NewString()}, nil }
	_, err = workSystemTranslationDocumentFence(workID)(t.Context(), store, uuid.New())
	require.ErrorContains(t, err, "changed during translation apply")
}
