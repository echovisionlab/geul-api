package public

import (
	"testing"
	"time"

	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFormDashboardUsesSchemaLabels(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	formID := "form-dashboard-labels"
	form := &model.Form{
		ID:        formID,
		IsPublic:  true,
		CreatedAt: now,
	}
	sourceDocument := &formdomain.FormSourceDocument{
		Title: "문의 폼",
		Schema: []byte(`{
			"id": "schema-1",
			"steps": [
				{
					"id": "step-1",
					"title": "Contact",
					"fields": [
						{ "id": "field-email", "key": "email", "label": "Email address", "type": "email" }
					]
				}
			]
		}`),
	}
	submissions := []model.FormSubmission{{
		ID:        "submission-1",
		FormID:    formID,
		Data:      []byte(`{"email":"johndoe@example.com"}`),
		CreatedAt: now,
	}}

	dashboard := buildFormDashboardFromSubmissions(form, sourceDocument, submissions, now)

	assert.Equal(t, "문의 폼", dashboard.FormTitle)
	require.Len(t, dashboard.FieldStats, 1)
	assert.Equal(t, "email", dashboard.FieldStats[0].FieldId)
	assert.Equal(t, "Email address", dashboard.FieldStats[0].FieldLabel)
	assert.Equal(t, "johndoe@example.com", dashboard.FieldStats[0].Values[0].Value)
}

func TestBuildFormDashboardLabelsSystemSubmissionMetadata(t *testing.T) {
	now := time.Unix(1_700_000_100, 0).UTC()
	formID := "form-dashboard-meta"
	form := &model.Form{
		ID:        formID,
		IsPublic:  true,
		CreatedAt: now,
	}
	sourceDocument := &formdomain.FormSourceDocument{
		Title:  "Contact form",
		Schema: []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email address","type":"email"}]}]}`),
	}
	submissions := []model.FormSubmission{{
		ID:          "submission-1",
		FormID:      formID,
		Data:        []byte(`{"email":"johndoe@example.com","__meta.locale":"ko","__meta.countryCode":"KR","__meta.timeZone":"Asia/Seoul"}`),
		CountryCode: new("KR"),
		CreatedAt:   now,
	}}

	dashboard := buildFormDashboardFromSubmissions(form, sourceDocument, submissions, now)

	require.Len(t, dashboard.FieldStats, 4)
	assert.Equal(t, "Country code", dashboard.FieldStats[0].FieldLabel)
	assert.Equal(t, "KR", dashboard.FieldStats[0].Values[0].Value)
	assert.Equal(t, "Locale", dashboard.FieldStats[1].FieldLabel)
	assert.Equal(t, "ko", dashboard.FieldStats[1].Values[0].Value)
	assert.Equal(t, "Time zone", dashboard.FieldStats[2].FieldLabel)
	assert.Equal(t, "Asia/Seoul", dashboard.FieldStats[2].Values[0].Value)
	assert.Equal(t, "Email address", dashboard.FieldStats[3].FieldLabel)
}

func TestBuildFormDashboardAggregatesTopValuesAndSkipsUnsupportedPayloads(t *testing.T) {
	now := time.Unix(1_700_000_200, 0).UTC()
	formID := "form-dashboard-top-values"
	form := &model.Form{
		ID:        formID,
		IsPublic:  true,
		CreatedAt: now,
	}
	sourceDocument := &formdomain.FormSourceDocument{
		Title:  "Dashboard Top Values Form",
		Schema: []byte(`{"id":"schema","steps":[{"id":"step","fields":[{"id":"choice","key":"choice","label":"Choice"},{"id":"enabled","key":"enabled","label":"Enabled"},{"id":"score","key":"score","label":"Score"}]}]}`),
	}
	values := []string{"k", "j", "i", "h", "g", "f", "e", "d", "c", "b", "a"}
	submissions := make([]model.FormSubmission, 0, len(values))
	for index, value := range values {
		submissions = append(submissions, model.FormSubmission{
			ID:        value,
			FormID:    formID,
			Data:      []byte(`{"choice":"` + value + `","score":` + string(rune('1'+index%3)) + `,"enabled":true,"ignored":{"nested":true}}`),
			CreatedAt: now.Add(time.Duration(index) * time.Minute),
		})
	}

	dashboard := buildFormDashboardFromSubmissions(form, sourceDocument, submissions, now)

	require.Equal(t, int32(11), dashboard.GetTotalSubmissions())
	statsByID := make(map[string]*openv1.FormDashboardFieldStat)
	for _, stat := range dashboard.GetFieldStats() {
		statsByID[stat.GetFieldId()] = stat
	}
	require.Contains(t, statsByID, "choice")
	require.Len(t, statsByID["choice"].GetValues(), 10)
	require.Equal(t, "a", statsByID["choice"].GetValues()[0].GetValue())
	require.Equal(t, "j", statsByID["choice"].GetValues()[9].GetValue())
	require.NotContains(t, statsByID, "ignored")
	require.Contains(t, statsByID, "enabled")
	require.Equal(t, "true", statsByID["enabled"].GetValues()[0].GetValue())
	require.Contains(t, statsByID, "score")
}
