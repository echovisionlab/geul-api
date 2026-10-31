package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAssignWorkUpdateRangeOnlyWritesTouchedValues(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := model.Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	year := int32(2026)

	fields, err := assignWorkUpdateRange(current, UpdateWorkCommand{Year: &year})

	require.NoError(t, err)
	require.Equal(t, int32(2026), fields["year"])
	require.NotContains(t, fields, "month")
	require.NotContains(t, fields, "until_year")
	require.NotContains(t, fields, "until_month")
	require.NotContains(t, fields, "is_present")
}

func TestAssignWorkUpdateRangeValidatesAgainstLockedCurrentValues(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := model.Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	newYear := int32(2027)

	_, err := assignWorkUpdateRange(current, UpdateWorkCommand{Year: &newYear})

	require.Error(t, err, "the locked month 8 makes the current end date earlier than the requested start")
}

func TestAssignWorkUpdateRangePresentClearsEndDatePair(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := model.Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	isPresent := true

	fields, err := assignWorkUpdateRange(current, UpdateWorkCommand{IsPresent: &isPresent})

	require.NoError(t, err)
	require.Equal(t, true, fields["is_present"])
	require.Contains(t, fields, "until_year")
	require.Nil(t, fields["until_year"])
	require.Contains(t, fields, "until_month")
	require.Nil(t, fields["until_month"])
	require.NotContains(t, fields, "year")
	require.NotContains(t, fields, "month")
}

func TestAssignWorkUpdateRangeUsesCurrentOtherEndComponent(t *testing.T) {
	untilYear, untilMonth := int32(2027), int32(2)
	current := model.Work{
		Year: 2026, Month: 8,
		UntilYear: &untilYear, UntilMonth: &untilMonth,
	}
	newUntilYear := int32(2028)

	fields, err := assignWorkUpdateRange(current, UpdateWorkCommand{UntilYear: &newUntilYear})

	require.NoError(t, err)
	require.Equal(t, &newUntilYear, fields["until_year"])
	require.NotContains(t, fields, "until_month")
}
