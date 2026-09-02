package emailauthoring

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEmailTemplateDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	t.Parallel()
	locale, err := normalizeEmailTemplateDocumentLocale("pt-BR")
	require.NoError(t, err)
	require.Equal(t, "pt-BR", locale)
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "pt"} {
		_, err := normalizeEmailTemplateDocumentLocale(input)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), input)
	}
}

func TestEmailTemplateTargetBatchRequiresExplicitEmptyInsteadOfLocaleDelete(t *testing.T) {
	t.Parallel()
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "fr", Deletes: []uuid.UUID{blockID}}},
	}
	require.Error(t, validateEmailTemplateTargetBatch(batch, documentID, revision, "fr", false))
	require.NoError(t, validateEmailTemplateTargetBatch(batch, documentID, revision, "fr", true))

	batch.LocaleGroups[0].Deletes = nil
	batch.LocaleGroups[0].Upserts = []contentblock.LocaleBlockUpdate{{
		BlockID: blockID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"content":[]}`),
	}}
	require.NoError(t, validateEmailTemplateTargetBatch(batch, documentID, revision, "fr", false))
}
