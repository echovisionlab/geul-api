package model

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

func TestDocumentLayoutProtoMappingRejectsUnspecifiedValues(t *testing.T) {
	t.Parallel()

	valid := &commonv1.DocumentLayout{
		ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT,
		PageChrome:    commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
		Footer:        commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
	}
	mapped, err := DocumentLayoutFromProto(valid)
	require.NoError(t, err)
	assert.Equal(t, DocumentContentHeightViewport, mapped.ContentHeight)
	assert.Equal(t, DocumentRegionPlacementPinned, mapped.PageChrome)
	assert.Equal(t, DocumentRegionPlacementFlow, mapped.Footer)
	assert.Equal(t, valid, mapped.Proto())

	alternate := &commonv1.DocumentLayout{
		ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT,
		PageChrome:    commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
		Footer:        commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
	}
	alternateMapped, err := DocumentLayoutFromProto(alternate)
	require.NoError(t, err)
	assert.Equal(t, alternate, alternateMapped.Proto())

	for name, layout := range map[string]*commonv1.DocumentLayout{
		"nil": nil,
		"content height": {
			PageChrome: commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
			Footer:     commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
		},
		"page chrome": {
			ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT,
			Footer:        commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
		},
		"footer": {
			ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT,
			PageChrome:    commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := DocumentLayoutFromProto(layout)
			assert.Error(t, err)
		})
	}

	assert.Panics(t, func() {
		_ = (DocumentLayout{}).Proto()
	})
}
