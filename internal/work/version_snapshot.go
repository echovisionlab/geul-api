// Package work owns Work-specific domain behavior.
package work

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
// Work Version checkpoint.
type VersionContentSnapshot struct {
	SchemaVersion int
	SourceLocale  string
	Title         *string
	Summary       *string
	Document      json.RawMessage
}

type versionContentSnapshotJSON struct {
	SchemaVersion int             `json:"schemaVersion"`
	SourceLocale  string          `json:"sourceLocale"`
	Title         *string         `json:"title"`
	Summary       *string         `json:"summary"`
	Document      json.RawMessage `json:"document"`
}

// EncodeVersionContentSnapshot serializes the canonical typed source document
// that a Work Version checkpoint restores.
func EncodeVersionContentSnapshot(
	sourceLocale string,
	title *string,
	summary *string,
	document *contentv1.LocalizedRichTextDocument,
) ([]byte, error) {
	sourceLocale = strings.TrimSpace(sourceLocale)
	if sourceLocale == "" {
		return nil, fmt.Errorf("work version source locale is required")
	}
	if document == nil {
		return nil, fmt.Errorf("work version typed document is required")
	}
	if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK {
		return nil, fmt.Errorf("work version document profile must be Work")
	}
	if document.GetLocale() != sourceLocale {
		return nil, fmt.Errorf("work version document locale does not match its source locale")
	}

	documentJSON, err := (protojson.MarshalOptions{UseProtoNames: false}).Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode Work version typed document: %w", err)
	}
	envelope, err := json.Marshal(versionContentSnapshotJSON{
		SchemaVersion: versionContentSnapshotSchemaVersion,
		SourceLocale:  sourceLocale,
		Title:         title,
		Summary:       summary,
		Document:      documentJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Work version snapshot: %w", err)
	}
	return envelope, nil
}

// DecodeVersionContentSnapshot validates and reads a Work Version checkpoint.
func DecodeVersionContentSnapshot(data []byte) (VersionContentSnapshot, *contentv1.LocalizedRichTextDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope versionContentSnapshotJSON
	if err := decoder.Decode(&envelope); err != nil {
		return VersionContentSnapshot{}, nil, fmt.Errorf("decode Work version snapshot: %w", err)
	}
	if err := requireSnapshotEOF(decoder); err != nil {
		return VersionContentSnapshot{}, nil, err
	}
	if envelope.SchemaVersion != versionContentSnapshotSchemaVersion {
		return VersionContentSnapshot{}, nil, fmt.Errorf("unsupported Work version snapshot schema %d", envelope.SchemaVersion)
	}
	envelope.SourceLocale = strings.TrimSpace(envelope.SourceLocale)
	if envelope.SourceLocale == "" {
		return VersionContentSnapshot{}, nil, fmt.Errorf("work version source locale is required")
	}
	if len(envelope.Document) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Document), []byte("null")) {
		return VersionContentSnapshot{}, nil, fmt.Errorf("work version typed document is required")
	}

	document := &contentv1.LocalizedRichTextDocument{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(envelope.Document, document); err != nil {
		return VersionContentSnapshot{}, nil, fmt.Errorf("decode Work version typed document: %w", err)
	}
	if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK {
		return VersionContentSnapshot{}, nil, fmt.Errorf("work version document profile must be Work")
	}
	if document.GetLocale() != envelope.SourceLocale {
		return VersionContentSnapshot{}, nil, fmt.Errorf("work version document locale does not match its source locale")
	}

	return VersionContentSnapshot{
		SchemaVersion: envelope.SchemaVersion,
		SourceLocale:  envelope.SourceLocale,
		Title:         envelope.Title,
		Summary:       envelope.Summary,
		Document:      append(json.RawMessage(nil), envelope.Document...),
	}, document, nil
}

func requireSnapshotEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("work version snapshot contains trailing JSON")
	}
	return fmt.Errorf("decode Work version snapshot trailing data: %w", err)
}
