package form

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOverlayFormLocalizationRowFallsBackOnlyForMissingUnits(t *testing.T) {
	sourceTitle := "Source"
	sourceText := "Source text"
	empty := ""

	row, fallback, err := overlayFormLocalizationRow(
		formLocalizationRow{
			Title:       &sourceTitle,
			ContentJSON: []byte(`{"source":true}`),
			ContentText: &sourceText,
		},
		formLocalizationRow{
			Title:       &empty,
			ContentJSON: []byte{},
		},
	)
	require.NoError(t, err)

	require.True(t, fallback)
	require.NotNil(t, row.Title)
	require.Empty(t, *row.Title, "explicit empty title must not fall back")
	require.NotNil(t, row.ContentJSON)
	require.Empty(t, row.ContentJSON, "explicit empty JSON must not fall back")
	require.Equal(t, &sourceText, row.ContentText, "missing text must fall back")
}

func TestOverlayFormLocalizationRowMaterializesSparseSchema(t *testing.T) {
	sourceText := "Contact\nEmail\nHelp"
	row, fallback, err := overlayFormLocalizationRow(
		formLocalizationRow{
			Title:       stringPointerForLocalization("Contact"),
			ContentJSON: []byte(`{"id":"schema","steps":[{"id":"step-a","title":"Contact","fields":[{"id":"field-a","label":"Email","description":"Help"}]}]}`),
			ContentText: &sourceText,
		},
		formLocalizationRow{
			ContentJSON: []byte(`{"id":"schema","steps":[{"id":"step-a","title":"연락처","fields":[{"id":"field-a","label":"이메일"}]}]}`),
		},
	)
	require.NoError(t, err)
	require.True(t, fallback)
	require.JSONEq(t, `{"id":"schema","steps":[{"id":"step-a","title":"연락처","fields":[{"id":"field-a","label":"이메일","description":"Help"}]}]}`, string(row.ContentJSON))
	require.Equal(t, "연락처\n이메일\nHelp", *row.ContentText)
}

func stringPointerForLocalization(value string) *string { return &value }
