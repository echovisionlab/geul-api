package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"

	"github.com/echovisionlab/geul-api/internal/structured"
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
	ContentHeight       DocumentContentHeight   `json:"contentHeight"`
	PageChrome          DocumentRegionPlacement `json:"pageChrome"`
	Footer              DocumentRegionPlacement `json:"footer"`
	HideTableOfContents bool                    `json:"hideTableOfContents,omitempty"`
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
		if err != nil {
			return fmt.Errorf("decode document layout: multiple JSON values: %w", err)
		}
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
