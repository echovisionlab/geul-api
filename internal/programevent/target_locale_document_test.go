package programevent

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNormalizeProgramEventDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	t.Parallel()
	locale, err := normalizeProgramEventDocumentLocale("zh-CN")
	require.NoError(t, err)
	require.Equal(t, "zh-CN", locale)
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		_, err := normalizeProgramEventDocumentLocale(input)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), input)
	}
}

func TestProgramEventTargetBatchRequiresExplicitEmptyInsteadOfLocaleDelete(t *testing.T) {
	t.Parallel()
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{blockID}}},
	}
	require.Error(t, validateProgramEventTargetBatch(batch, documentID, revision, "ko", false))
	require.NoError(t, validateProgramEventTargetBatch(batch, documentID, revision, "ko", true))

	batch.LocaleGroups[0].Deletes = nil
	batch.LocaleGroups[0].Upserts = []contentblock.LocaleBlockUpdate{{
		BlockID: blockID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"content":[]}`),
	}}
	require.NoError(t, validateProgramEventTargetBatch(batch, documentID, revision, "ko", false))
}
