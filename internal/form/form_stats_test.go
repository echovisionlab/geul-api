package form

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFormSubmissionStatsAggregatesValuesAndSchemaLabels(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 24, 15, 0, 0, 0, time.UTC)
	stats := buildFormSubmissionStats(
		[]byte(`{
			"steps": [{
				"fields": [
					{ "id": "field-email", "key": "email", "label": "Email address" },
					{ "id": "field-topic", "key": "topic", "label": "Topic" }
				]
			}]
		}`),
		[]formSubmissionStatsRow{
			{CreatedAt: now.Add(-time.Hour), Data: []byte(`{"email":"johndoe@example.com","topic":"support","ignored":["x"]}`)},
			{CreatedAt: now.AddDate(0, 0, -2), Data: []byte(`{"email":"johndoe@example.com","topic":"sales"}`)},
			{CreatedAt: now.AddDate(0, -1, 0), Data: []byte(`{"email":"other@example.com","topic":true}`)},
			{CreatedAt: now, Data: []byte(`not-json`)},
		},
		now,
	)

	require.NotNil(t, stats)
	assert.Equal(t, int32(4), stats.TotalSubmissions)
	assert.Equal(t, int32(2), stats.SubmissionsToday)
	assert.Equal(t, int32(3), stats.SubmissionsThisWeek)
	assert.Equal(t, int32(3), stats.SubmissionsThisMonth)
	require.Len(t, stats.FieldStats, 2)
	assert.Equal(t, "email", stats.FieldStats[0].FieldId)
	assert.Equal(t, "Email address", stats.FieldStats[0].FieldLabel)
	assert.Equal(t, "johndoe@example.com", stats.FieldStats[0].Values[0].Value)
	assert.Equal(t, int32(2), stats.FieldStats[0].Values[0].Count)
	assert.Equal(t, "topic", stats.FieldStats[1].FieldId)
	assert.Equal(t, "Topic", stats.FieldStats[1].FieldLabel)
	assert.Equal(t, "sales", stats.FieldStats[1].Values[0].Value)
}
