package entityparticipant

import (
	"testing"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/require"
)

func TestParseNullableStringUpdateRequiresExplicitOperation(t *testing.T) {
	_, err := ParseNullableStringUpdate("slug", &intrav1.NullableStringMutation{})
	require.Error(t, err)
	_, err = ParseNullableStringUpdate("slug", &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Set{Set: "  "}})
	require.Error(t, err)
	update, err := ParseNullableStringUpdate("slug", &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Clear{Clear: true}})
	require.NoError(t, err)
	require.True(t, update.Present)
	require.Nil(t, update.Value)
}
