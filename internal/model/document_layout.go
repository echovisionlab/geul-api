package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"

	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type DocumentContentHeight string

const (
	DocumentContentHeightContent  DocumentContentHeight = "content"
	DocumentContentHeightViewport DocumentContentHeight = "viewport"
)

type DocumentRegionPlacement string

const (
	DocumentRegionPlacementFlow   DocumentRegionPlacement = "flow"
	DocumentRegionPlacementPinned DocumentRegionPlacement = "pinned"
)

// DocumentLayout is the strict JSONB representation shared by page and post roots.
type DocumentLayout struct {
	ContentHeight DocumentContentHeight   `json:"contentHeight"`
	PageChrome    DocumentRegionPlacement `json:"pageChrome"`
	Footer        DocumentRegionPlacement `json:"footer"`
}

func DefaultDocumentLayout() DocumentLayout {
	return DocumentLayout{
		ContentHeight: DocumentContentHeightContent,
		PageChrome:    DocumentRegionPlacementFlow,
		Footer:        DocumentRegionPlacementFlow,
	}
}

func (l DocumentLayout) Validate() error {
	switch l.ContentHeight {
	case DocumentContentHeightContent, DocumentContentHeightViewport:
	default:
		return fmt.Errorf("invalid contentHeight %q", l.ContentHeight)
	}
	switch l.PageChrome {
	case DocumentRegionPlacementFlow, DocumentRegionPlacementPinned:
	default:
		return fmt.Errorf("invalid pageChrome %q", l.PageChrome)
	}
	switch l.Footer {
	case DocumentRegionPlacementFlow, DocumentRegionPlacementPinned:
	default:
		return fmt.Errorf("invalid footer %q", l.Footer)
	}
	return nil
}

func (l *DocumentLayout) Scan(value structured.Value) error {
	if value == nil {
		return fmt.Errorf("document layout cannot be null")
	}

	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("cannot scan %T into DocumentLayout", value)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decoded DocumentLayout
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode document layout: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode document layout: multiple JSON values")
	}
	if err := decoded.Validate(); err != nil {
		return err
	}

	*l = decoded
	return nil
}

func (l DocumentLayout) Value() (driver.Value, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(l)
}

func DocumentLayoutFromProto(layout *commonv1.DocumentLayout) (DocumentLayout, error) {
	if layout == nil {
		return DocumentLayout{}, fmt.Errorf("document layout is required")
	}

	result := DocumentLayout{}
	switch layout.ContentHeight {
	case commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT:
		result.ContentHeight = DocumentContentHeightContent
	case commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT:
		result.ContentHeight = DocumentContentHeightViewport
	default:
		return DocumentLayout{}, fmt.Errorf("content_height must be CONTENT or VIEWPORT")
	}
	switch layout.PageChrome {
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW:
		result.PageChrome = DocumentRegionPlacementFlow
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED:
		result.PageChrome = DocumentRegionPlacementPinned
	default:
		return DocumentLayout{}, fmt.Errorf("page_chrome must be FLOW or PINNED")
	}
	switch layout.Footer {
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW:
		result.Footer = DocumentRegionPlacementFlow
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED:
		result.Footer = DocumentRegionPlacementPinned
	default:
		return DocumentLayout{}, fmt.Errorf("footer must be FLOW or PINNED")
	}

	return result, nil
}

// Proto maps a validated persisted layout without producing unspecified enum values.
func (l DocumentLayout) Proto() *commonv1.DocumentLayout {
	if err := l.Validate(); err != nil {
		panic(fmt.Sprintf("invalid persisted document layout: %v", err))
	}

	result := &commonv1.DocumentLayout{}
	switch l.ContentHeight {
	case DocumentContentHeightContent:
		result.ContentHeight = commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT
	case DocumentContentHeightViewport:
		result.ContentHeight = commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT
	}
	switch l.PageChrome {
	case DocumentRegionPlacementFlow:
		result.PageChrome = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW
	case DocumentRegionPlacementPinned:
		result.PageChrome = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED
	}
	switch l.Footer {
	case DocumentRegionPlacementFlow:
		result.Footer = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW
	case DocumentRegionPlacementPinned:
		result.Footer = commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED
	}
	return result
}
