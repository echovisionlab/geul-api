package documentlayout

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestDocumentLayoutProtoMappingRejectsUnspecifiedValues(t *testing.T) {
	t.Parallel()

	valid := &commonv1.DocumentLayout{
		HideTableOfContents: true,
		ContentHeight:       commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT,
		PageChrome:          commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
		Footer:              commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
	}
	mapped, err := FromProto(valid)
	require.NoError(t, err)
	assert.Equal(t, model.DocumentContentHeightViewport, mapped.ContentHeight)
	assert.Equal(t, model.DocumentRegionPlacementPinned, mapped.PageChrome)
	assert.Equal(t, model.DocumentRegionPlacementFlow, mapped.Footer)
	assert.Equal(t, valid, requireDocumentLayoutProto(t, mapped))
	persisted, err := mapped.Value()
	require.NoError(t, err)
	var loaded model.DocumentLayout
	require.NoError(t, loaded.Scan(persisted))
	assert.True(t, loaded.HideTableOfContents)
	assert.Equal(t, valid, requireDocumentLayoutProto(t, loaded))

	alternate := &commonv1.DocumentLayout{
		ContentHeight: commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT,
		PageChrome:    commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW,
		Footer:        commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED,
	}
	alternateMapped, err := FromProto(alternate)
	require.NoError(t, err)
	assert.Equal(t, alternate, requireDocumentLayoutProto(t, alternateMapped))

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
			_, err := FromProto(layout)
			assert.Error(t, err)
		})
	}

	_, err = ToProto(model.DocumentLayout{})
	assert.Error(t, err)
}

func TestDocumentLayoutProtoReturnsErrorForInvalidPersistedLayout(t *testing.T) {
	t.Parallel()

	for name, layout := range map[string]model.DocumentLayout{
		"zero":           {},
		"content height": {ContentHeight: "bogus", PageChrome: model.DocumentRegionPlacementFlow, Footer: model.DocumentRegionPlacementFlow},
		"page chrome":    {ContentHeight: model.DocumentContentHeightContent, PageChrome: "bogus", Footer: model.DocumentRegionPlacementFlow},
		"footer":         {ContentHeight: model.DocumentContentHeightContent, PageChrome: model.DocumentRegionPlacementFlow, Footer: "bogus"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var mapped *commonv1.DocumentLayout
			var err error
			require.NotPanics(t, func() { mapped, err = ToProto(layout) })
			require.ErrorContains(t, err, "invalid persisted document layout")
			require.Nil(t, mapped)
		})
	}
}

func requireDocumentLayoutProto(t *testing.T, layout model.DocumentLayout) *commonv1.DocumentLayout {
	t.Helper()
	mapped, err := ToProto(layout)
	require.NoError(t, err)
	return mapped
}
