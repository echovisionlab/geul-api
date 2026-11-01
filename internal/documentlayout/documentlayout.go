// Package documentlayout maps the stored Page and Post document layout to
// and from the API message.
package documentlayout

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// FromProto maps an API layout to its stored form, rejecting unspecified
// values.
func FromProto(layout *commonv1.DocumentLayout) (model.DocumentLayout, error) {
	if layout == nil {
		return model.DocumentLayout{}, fmt.Errorf("document layout is required")
	}

	result := model.DocumentLayout{HideTableOfContents: layout.HideTableOfContents}
	switch layout.ContentHeight {
	case commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT:
		result.ContentHeight = model.DocumentContentHeightContent
	case commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT:
		result.ContentHeight = model.DocumentContentHeightViewport
	default:
		return model.DocumentLayout{}, fmt.Errorf("content_height must be CONTENT or VIEWPORT")
	}
	switch layout.PageChrome {
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW:
		result.PageChrome = model.DocumentRegionPlacementFlow
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED:
		result.PageChrome = model.DocumentRegionPlacementPinned
	default:
		return model.DocumentLayout{}, fmt.Errorf("page_chrome must be FLOW or PINNED")
	}
	switch layout.Footer {
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW:
		result.Footer = model.DocumentRegionPlacementFlow
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED:
		result.Footer = model.DocumentRegionPlacementPinned
	default:
		return model.DocumentLayout{}, fmt.Errorf("footer must be FLOW or PINNED")
	}

	return result, nil
}

// ToProto maps a validated persisted layout without producing unspecified
// enum values. An invalid stored layout is an error so read paths can reject
// it without crashing the request.
func ToProto(l model.DocumentLayout) (*commonv1.DocumentLayout, error) {
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("invalid persisted document layout: %w", err)
	}

	result := &commonv1.DocumentLayout{HideTableOfContents: l.HideTableOfContents}
	switch l.ContentHeight {
	case model.DocumentContentHeightContent:
		result.ContentHeight = commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT
	case model.DocumentContentHeightViewport:
		result.ContentHeight = commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT
	}
	switch l.PageChrome {
	case model.DocumentRegionPlacementFlow:
		result.PageChrome = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW
	case model.DocumentRegionPlacementPinned:
		result.PageChrome = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED
	}
	switch l.Footer {
	case model.DocumentRegionPlacementFlow:
		result.Footer = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW
	case model.DocumentRegionPlacementPinned:
		result.Footer = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED
	}
	return result, nil
}
