package campaign

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCampaignDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	t.Parallel()
	locale, err := normalizeCampaignDocumentLocale("es-419")
	require.NoError(t, err)
	require.Equal(t, "es-419", locale)
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		_, err := normalizeCampaignDocumentLocale(locale)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), locale)
	}
}

func TestCampaignTargetBatchRequiresExplicitEmptyInsteadOfLocaleDelete(t *testing.T) {
	t.Parallel()
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ja", Deletes: []uuid.UUID{blockID}}},
	}
	require.Error(t, validateCampaignTargetBatch(batch, documentID, revision, "ja", false))
	require.NoError(t, validateCampaignTargetBatch(batch, documentID, revision, "ja", true))

	batch.LocaleGroups[0].Deletes = nil
	batch.LocaleGroups[0].Upserts = []contentblock.LocaleBlockUpdate{{
		BlockID: blockID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"content":[]}`),
	}}
	require.NoError(t, validateCampaignTargetBatch(batch, documentID, revision, "ja", false))
}
