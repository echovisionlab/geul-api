package work

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssignWorkUpdateRangeOnlyWritesTouchedValues(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	year := int32(2026)

	var change WorkChange
	err := assignWorkUpdateRange(current, UpdateWorkCommand{Year: &year}, &change)

	require.NoError(t, err)
	require.Equal(t, int32(2026), *change.Year)
	require.Nil(t, change.Month)
	require.False(t, change.UntilYear.Set)
	require.False(t, change.UntilMonth.Set)
	require.Nil(t, change.IsPresent)
}

func TestAssignWorkUpdateRangeValidatesAgainstLockedCurrentValues(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	newYear := int32(2027)

	err := assignWorkUpdateRange(current, UpdateWorkCommand{Year: &newYear}, &WorkChange{})

	require.Error(t, err, "the locked month 8 makes the current end date earlier than the requested start")
}

func TestAssignWorkUpdateRangePresentClearsEndDatePair(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	isPresent := true

	var change WorkChange
	err := assignWorkUpdateRange(current, UpdateWorkCommand{IsPresent: &isPresent}, &change)

	require.NoError(t, err)
	require.True(t, *change.IsPresent)
	require.Equal(t, OptionalInt32{Set: true}, change.UntilYear)
	require.Equal(t, OptionalInt32{Set: true}, change.UntilMonth)
	require.Nil(t, change.Year)
	require.Nil(t, change.Month)
}

func TestAssignWorkUpdateRangeUsesCurrentOtherEndComponent(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	newUntilYear := int32(2028)

	var change WorkChange
	err := assignWorkUpdateRange(current, UpdateWorkCommand{UntilYear: &newUntilYear}, &change)

	require.NoError(t, err)
	require.Equal(t, &newUntilYear, change.UntilYear.Value)
	require.False(t, change.UntilMonth.Set)
}
