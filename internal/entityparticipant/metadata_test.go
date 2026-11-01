package entityparticipant

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseNullableStringUpdateRequiresExplicitOperation(t *testing.T) {
	update, err := ParseNullableStringUpdate("slug", nil)
	require.NoError(t, err)
	require.False(t, update.Present)
	_, err = ParseNullableStringUpdate("slug", &StringMutation{})
	require.EqualError(t, err, "slug: set or clear is required")
	blank := "  "
	_, err = ParseNullableStringUpdate("slug", &StringMutation{Set: &blank})
	require.EqualError(t, err, "slug: set value must not be empty; use clear instead")
	clear := false
	_, err = ParseNullableStringUpdate("slug", &StringMutation{Clear: &clear})
	require.EqualError(t, err, "slug: clear must be true")
	clear = true
	update, err = ParseNullableStringUpdate("slug", &StringMutation{Clear: &clear})
	require.NoError(t, err)
	require.True(t, update.Present)
	require.Nil(t, update.Value)
	set := " label "
	update, err = ParseNullableStringUpdate("slug", &StringMutation{Set: &set})
	require.NoError(t, err)
	require.Equal(t, "label", *update.Value)
}
