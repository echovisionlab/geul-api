package post

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const postVersionContentSnapshotSchemaVersion = 1

type postVersionContentSnapshot struct {
	SchemaVersion int             `json:"schemaVersion"`
	SourceLocale  string          `json:"sourceLocale"`
	Title         *string         `json:"title"`
	Summary       *string         `json:"summary"`
	Document      json.RawMessage `json:"document"`
}

func marshalPostVersionContentSnapshot(
	document *contentv1.LocalizedRichTextDocument,
	sourceLocale string,
	title *string,
	summary *string,
) (json.RawMessage, string, error) {
	if err := validatePostVersionLocalizedDocument(document, sourceLocale); err != nil {
		return nil, "", err
	}
	documentJSON, err := (protojson.MarshalOptions{}).Marshal(document)
	if err != nil {
		return nil, "", fmt.Errorf("encode Post Version typed document: %w", err)
	}
	envelope := postVersionContentSnapshot{
		SchemaVersion: postVersionContentSnapshotSchemaVersion,
		SourceLocale:  sourceLocale,
		Title:         cloneNullableString(title),
		Summary:       cloneNullableString(summary),
		Document:      documentJSON,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", fmt.Errorf("encode Post Version content snapshot: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func unmarshalPostVersionContentSnapshot(
	raw json.RawMessage,
) (postVersionContentSnapshot, *contentv1.LocalizedRichTextDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope postVersionContentSnapshot
	if err := decoder.Decode(&envelope); err != nil {
		return postVersionContentSnapshot{}, nil, fmt.Errorf("decode Post Version content snapshot: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return postVersionContentSnapshot{}, nil, fmt.Errorf("post Version content snapshot must contain one object")
	}
	if envelope.SchemaVersion != postVersionContentSnapshotSchemaVersion {
		return postVersionContentSnapshot{}, nil, fmt.Errorf("unsupported Post Version content snapshot schema %d", envelope.SchemaVersion)
	}
	if len(envelope.Document) == 0 || bytes.Equal(envelope.Document, []byte("null")) {
		return postVersionContentSnapshot{}, nil, fmt.Errorf("post Version typed document is required")
	}
	document := &contentv1.LocalizedRichTextDocument{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(envelope.Document, document); err != nil {
		return postVersionContentSnapshot{}, nil, fmt.Errorf("decode Post Version typed document: %w", err)
	}
	if err := validatePostVersionLocalizedDocument(document, envelope.SourceLocale); err != nil {
		return postVersionContentSnapshot{}, nil, err
	}
	return envelope, document, nil
}

func validatePostVersionLocalizedDocument(
	document *contentv1.LocalizedRichTextDocument,
	sourceLocale string,
) error {
	sourceLocale = strings.TrimSpace(sourceLocale)
	if sourceLocale == "" {
		return fmt.Errorf("post Version source locale is required")
	}
	if document == nil {
		return fmt.Errorf("post Version typed document is required")
	}
	if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST {
		return fmt.Errorf("post Version typed document must use the Post profile")
	}
	if document.GetLocale() != sourceLocale {
		return fmt.Errorf("post Version typed document locale does not match source locale")
	}
	if document.GetBase() == nil || document.GetLocaleOverlay() == nil {
		return fmt.Errorf("post Version typed document graph and source overlay are required")
	}
	if document.GetLocaleOverlay().GetLocale() != sourceLocale {
		return fmt.Errorf("post Version typed document overlay does not match source locale")
	}
	return nil
}

func cloneNullableString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
