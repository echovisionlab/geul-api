package series

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestPostSeriesAIDocumentSelectsOneExactActionPerCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		operations []core.Operation
		want       seriesAction
	}{
		{name: "copy", operations: []core.Operation{core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text("title"))}, want: policyv1.PostSeries.Edit},
		{name: "translation", operations: []core.Operation{core.CreateTranslationOperation()}, want: policyv1.PostSeries.Edit},
		{name: "publish", operations: []core.Operation{core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldStatus, core.Text("SERIES_STATUS_PUBLISHED"))}, want: policyv1.PostSeries.Publish},
		{name: "structure wins", operations: []core.Operation{
			core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldStatus, core.Text("SERIES_STATUS_PUBLISHED")),
			core.MoveRelationItemOperation(postSeriesAIDocumentBlock, postSeriesAIDocumentPosts, "post-1", postSeriesAIDocumentBlock, postSeriesAIDocumentPosts, ""),
		}, want: policyv1.PostSeries.Manage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const seriesID = "series-action-test"
			got, err := postSeriesAIDocumentAction(test.operations)(seriesID)
			require.NoError(t, err)
			want, err := test.want(seriesID)
			require.NoError(t, err)
			require.Equal(t, want.EngineKey(), got.EngineKey())
		})
	}
}
