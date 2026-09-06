package label

import (
	"testing"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/require"
)

func TestBuildLabelDocumentMetadataPlanUsesTypedClear(t *testing.T) {
	plan, err := buildLabelDocumentMetadataPlan(&intrav1.LabelDocumentMetadataUpdate{
		ParentLabelId: &intrav1.NullableStringMutation{
			Operation: &intrav1.NullableStringMutation_Clear{Clear: true},
		},
	})
	require.NoError(t, err)
	require.True(t, plan.parentLabelID.present)
	require.Nil(t, plan.parentLabelID.value)
}
