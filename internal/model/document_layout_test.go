package model

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultDocumentLayout(t *testing.T) {
	t.Parallel()

	layout := DefaultDocumentLayout()
	require.NoError(t, layout.Validate())
	assert.Equal(t, DocumentContentHeightContent, layout.ContentHeight)
	assert.Equal(t, DocumentRegionPlacementFlow, layout.PageChrome)
	assert.Equal(t, DocumentRegionPlacementFlow, layout.Footer)

	value, err := layout.Value()
	require.NoError(t, err)
	assert.JSONEq(t, `{"contentHeight":"content","pageChrome":"flow","footer":"flow"}`, string(value.([]byte)))
}

func TestDocumentLayoutScanAcceptsOnlyStrictCanonicalShape(t *testing.T) {
	t.Parallel()

	for _, input := range (structured.Values{
		[]byte(`{"contentHeight":"viewport","pageChrome":"pinned","footer":"flow"}`),
		`{"contentHeight":"viewport","pageChrome":"pinned","footer":"flow"}`,
	}) {
		var layout DocumentLayout
		require.NoError(t, layout.Scan(input))
		assert.Equal(t, DocumentLayout{
			ContentHeight: DocumentContentHeightViewport,
			PageChrome:    DocumentRegionPlacementPinned,
			Footer:        DocumentRegionPlacementFlow,
		}, layout)
	}

	for name, input := range (structured.Fields{
		"null":          nil,
		"unsupported":   42,
		"empty":         []byte{},
		"json null":     []byte(`null`),
		"missing field": []byte(`{"contentHeight":"content","pageChrome":"flow"}`),
		"extra field":   []byte(`{"contentHeight":"content","pageChrome":"flow","footer":"flow","extra":true}`),
		"unknown value": []byte(`{"contentHeight":"content","pageChrome":"flow","footer":"sticky"}`),
		"trailing value": []byte(
			`{"contentHeight":"content","pageChrome":"flow","footer":"flow"} {}`,
		),
	}) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var layout DocumentLayout
			assert.Error(t, layout.Scan(input))
		})
	}
}

func TestDocumentLayoutValueRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	for name, layout := range map[string]DocumentLayout{
		"content height": {
			ContentHeight: "auto",
			PageChrome:    DocumentRegionPlacementFlow,
			Footer:        DocumentRegionPlacementFlow,
		},
		"page chrome": {
			ContentHeight: DocumentContentHeightContent,
			PageChrome:    "sticky",
			Footer:        DocumentRegionPlacementFlow,
		},
		"footer": {
			ContentHeight: DocumentContentHeightContent,
			PageChrome:    DocumentRegionPlacementFlow,
			Footer:        "sticky",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := layout.Value()
			assert.Error(t, err)
		})
	}
}
