package post

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func TestPostDocumentIdentityRequiresExactCanonicalLocale(t *testing.T) {
	postID := uuid.NewString()
	locale, err := validatePostAIDocumentIdentity(postID, "pt-PT")
	require.NoError(t, err)
	require.Equal(t, "pt-PT", locale)
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "pt"} {
		_, err := validatePostAIDocumentIdentity(postID, input)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), input)
	}
}

func TestPostTargetLocaleValueDeleteRequiresInternalReplacementAuthority(t *testing.T) {
	documentID := uuid.New()
	blockID := uuid.New()
	storage := &contentv1.ContentStorageMutationBatch{
		ExpectedRevision: documentID.String(),
		LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{{
			Locale:  "en",
			Deletes: []string{blockID.String()},
		}},
	}

	require.Error(t, validatePostTargetStorage(storage, documentID, "en", false))
	require.NoError(t, validatePostTargetStorage(storage, documentID, "en", true))

	batch := contentblock.Batch{
		DocumentID:       documentID,
		ExpectedRevision: documentID,
		LocaleGroups: []contentblock.LocaleMutationGroup{{
			Locale:  "en",
			Deletes: []uuid.UUID{blockID},
		}},
	}
	require.Error(t, validatePostTargetBatch(batch, documentID, documentID, "en", false))
	require.NoError(t, validatePostTargetBatch(batch, documentID, documentID, "en", true))
}
