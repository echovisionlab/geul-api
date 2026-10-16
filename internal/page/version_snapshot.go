// Package page owns Page-specific domain behavior.
package page

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const versionContentSnapshotSchemaVersion = 1

// VersionContentSnapshot is the canonical source-owned content recorded by a
// Page Version checkpoint.
type VersionContentSnapshot struct {
	SchemaVersion int
	SourceLocale  string
	Title         *string
	Summary       *string
	Document      *contentv1.LocalizedPageDocument
}

// EncodeVersionContentSnapshot serializes the canonical typed source document
// that a Page Version checkpoint restores.
func EncodeVersionContentSnapshot(
	sourceLocale string,
	title *string,
	summary *string,
	document *contentv1.LocalizedPageDocument,
) (json.RawMessage, error) {
	snapshot := VersionContentSnapshot{
		SchemaVersion: versionContentSnapshotSchemaVersion,
		SourceLocale:  sourceLocale,
		Title:         title,
		Summary:       summary,
		Document:      document,
	}
	if err := validateVersionContentSnapshot(&snapshot); err != nil {
		return nil, err
	}
	documentJSON, err := (protojson.MarshalOptions{}).Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode Page version typed document: %w", err)
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion int             `json:"schemaVersion"`
		SourceLocale  string          `json:"sourceLocale"`
		Title         *string         `json:"title"`
		Summary       *string         `json:"summary"`
		Document      json.RawMessage `json:"document"`
	}{
		SchemaVersion: snapshot.SchemaVersion,
		SourceLocale:  snapshot.SourceLocale,
		Title:         snapshot.Title,
		Summary:       snapshot.Summary,
		Document:      documentJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Page version content snapshot: %w", err)
	}
	return encoded, nil
}

// DecodeVersionContentSnapshot validates and reads a Page Version checkpoint.
func DecodeVersionContentSnapshot(encoded json.RawMessage) (VersionContentSnapshot, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return VersionContentSnapshot{}, fmt.Errorf("decode Page version content snapshot: %w", err)
	}
	for _, required := range []string{"schemaVersion", "sourceLocale", "title", "summary", "document"} {
		if _, exists := fields[required]; !exists {
			return VersionContentSnapshot{}, fmt.Errorf("page version content snapshot is missing %q", required)
		}
	}
	if len(fields) != 5 {
		return VersionContentSnapshot{}, fmt.Errorf("page version content snapshot contains unknown fields")
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire struct {
		SchemaVersion int             `json:"schemaVersion"`
		SourceLocale  string          `json:"sourceLocale"`
		Title         *string         `json:"title"`
		Summary       *string         `json:"summary"`
		Document      json.RawMessage `json:"document"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return VersionContentSnapshot{}, fmt.Errorf("decode Page version content snapshot: %w", err)
	}
	if err := consumeJSONEOF(decoder); err != nil {
		return VersionContentSnapshot{}, fmt.Errorf("decode Page version content snapshot: %w", err)
	}
	document := &contentv1.LocalizedPageDocument{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(wire.Document, document); err != nil {
		return VersionContentSnapshot{}, fmt.Errorf("decode Page version typed document: %w", err)
	}
	snapshot := VersionContentSnapshot{
		SchemaVersion: wire.SchemaVersion,
		SourceLocale:  wire.SourceLocale,
		Title:         wire.Title,
		Summary:       wire.Summary,
		Document:      document,
	}
	if err := validateVersionContentSnapshot(&snapshot); err != nil {
		return VersionContentSnapshot{}, err
	}
	return snapshot, nil
}

func validateVersionContentSnapshot(snapshot *VersionContentSnapshot) error {
	if snapshot == nil || snapshot.SchemaVersion != versionContentSnapshotSchemaVersion {
		return fmt.Errorf("unsupported Page version content snapshot schema")
	}
	if snapshot.SourceLocale == "" || snapshot.SourceLocale != strings.TrimSpace(snapshot.SourceLocale) || len(snapshot.SourceLocale) > 35 {
		return fmt.Errorf("invalid Page version source locale")
	}
	if err := contentv1.ValidatePageDocument(
		&contentv1.PageDocument{
			BlockCatalogFingerprint: snapshot.Document.GetBlockCatalogFingerprint(),
			SourceLocale:            snapshot.Document.GetLocale(),
			Base:                    snapshot.Document.GetBase(),
			LocaleOverlays:          []*contentv1.PageLocaleOverlay{snapshot.Document.GetLocaleOverlay()},
		},
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	); err != nil {
		return fmt.Errorf("invalid Page version typed document: %w", err)
	}
	if snapshot.Document.GetLocale() != snapshot.SourceLocale ||
		snapshot.Document.GetLocaleOverlay().GetLocale() != snapshot.SourceLocale {
		return fmt.Errorf("page version typed document locale does not match source locale")
	}
	return nil
}

func consumeJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
